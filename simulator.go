package ftl

import "fmt"

// Config captures the flash geometry from the specification.
type Config struct {
	TotalBlocks   int
	PagesPerBlock int
}

// PageState tracks whether a page is unused, currently mapped, or obsolete.
type PageState int

const (
	PageStateFree PageState = iota
	PageStateValid
	PageStateInvalid
)

// BlockType identifies the role a block currently plays in the system.
type BlockType int

const (
	BlockTypeData BlockType = iota
	BlockTypeLog
	BlockTypeFree
)

// PhysicalPage stores the physical location of an LBA.
type PhysicalPage struct {
	BlockID    int
	PageOffset int
}

// Page represents a single flash page. LBA is nil when the page is free.
type Page struct {
	State PageState
	LBA   *int
}

// Block represents a flash block and its pages.
type Block struct {
	ID    int
	Type  BlockType
	Pages []Page
}

// Metrics captures the logical counters requested by the specification.
type Metrics struct {
	TotalLogicalWrites  uint64
	TotalPhysicalWrites uint64
	TotalErases         uint64
}

// WriteAmplification returns physical/logical writes, or 0 when no writes exist yet.
func (m Metrics) WriteAmplification() float64 {
	if m.TotalLogicalWrites == 0 {
		return 0
	}

	return float64(m.TotalPhysicalWrites) / float64(m.TotalLogicalWrites)
}

// Simulator is the in-memory state for the FAST-style hybrid FTL simulator.
type Simulator struct {
	Config Config

	Blocks  []Block
	Mapping map[int]PhysicalPage

	// V1 uses a single active log block.
	ActiveLogBlockID *int

	Metrics Metrics
}

// NewSimulator allocates the initial flash layout with all blocks and pages free.
func NewSimulator(cfg Config) *Simulator {
	blocks := make([]Block, cfg.TotalBlocks)
	for blockID := 0; blockID < cfg.TotalBlocks; blockID++ {
		pages := make([]Page, cfg.PagesPerBlock)
		for pageOffset := 0; pageOffset < cfg.PagesPerBlock; pageOffset++ {
			pages[pageOffset] = Page{State: PageStateFree}
		}

		blocks[blockID] = Block{
			ID:    blockID,
			Type:  BlockTypeFree,
			Pages: pages,
		}
	}

	return &Simulator{
		Config:  cfg,
		Blocks:  blocks,
		Mapping: make(map[int]PhysicalPage),
	}
}

// Write performs an out-of-place write through the active log block.
func (s *Simulator) Write(lba int) error {
	if lba < 0 {
		return fmt.Errorf("lba must be non-negative: %d", lba)
	}

	logicalBlockID := s.logicalBlockID(lba)

	logBlockID, err := s.prepareWritableLogBlock(logicalBlockID)
	if err != nil {
		return err
	}

	if oldLocation, ok := s.Mapping[lba]; ok {
		s.invalidatePhysicalPage(oldLocation)
	}

	pageOffset, err := s.appendToLogBlock(logBlockID, lba)
	if err != nil {
		return err
	}

	s.Mapping[lba] = PhysicalPage{
		BlockID:    logBlockID,
		PageOffset: pageOffset,
	}

	s.Metrics.TotalLogicalWrites++
	s.Metrics.TotalPhysicalWrites++

	return nil
}

func (s *Simulator) prepareWritableLogBlock(logicalBlockID int) (int, error) {
	if s.ActiveLogBlockID != nil {
		activeLogBlockID := *s.ActiveLogBlockID
		matches, err := s.activeLogBlockMatchesLogicalBlock(activeLogBlockID, logicalBlockID)
		if err != nil {
			return 0, err
		}

		if matches && s.nextFreePageOffset(activeLogBlockID) >= 0 {
			return activeLogBlockID, nil
		}

		if err := s.merge(); err != nil {
			return 0, err
		}
	}

	freeBlockID, ok := s.findFreeBlock()
	if !ok {
		return 0, fmt.Errorf("no free block available for log writes")
	}

	s.Blocks[freeBlockID].Type = BlockTypeLog
	s.ActiveLogBlockID = intPtr(freeBlockID)

	return freeBlockID, nil
}

func (s *Simulator) appendToLogBlock(blockID int, lba int) (int, error) {
	pageOffset := s.nextFreePageOffset(blockID)
	if pageOffset < 0 {
		return 0, fmt.Errorf("log block %d has no free pages", blockID)
	}

	s.Blocks[blockID].Pages[pageOffset] = Page{
		State: PageStateValid,
		LBA:   intPtr(lba),
	}

	return pageOffset, nil
}

func (s *Simulator) merge() error {
	if s.ActiveLogBlockID == nil {
		return nil
	}

	activeLogBlockID := *s.ActiveLogBlockID
	logicalBlockID, hasLogicalBlock, err := s.logBlockLogicalBlockID(activeLogBlockID)
	if err != nil {
		return err
	}

	if !hasLogicalBlock {
		s.eraseBlock(activeLogBlockID)
		s.ActiveLogBlockID = nil
		return nil
	}

	if s.canSwitchMerge(activeLogBlockID, logicalBlockID) {
		return s.switchMerge(activeLogBlockID, logicalBlockID)
	}

	return s.fullMerge(activeLogBlockID, logicalBlockID)
}

func (s *Simulator) switchMerge(logBlockID int, logicalBlockID int) error {
	if oldDataBlockID, ok := s.findDataBlockForLogicalBlock(logicalBlockID); ok {
		s.eraseBlock(oldDataBlockID)
	}

	s.Blocks[logBlockID].Type = BlockTypeData
	s.ActiveLogBlockID = nil

	return nil
}

func (s *Simulator) fullMerge(logBlockID int, logicalBlockID int) error {
	oldDataBlockID, hasOldDataBlock := s.findDataBlockForLogicalBlock(logicalBlockID)

	newDataBlockID, ok := s.findFreeBlock()
	if !ok {
		return fmt.Errorf("full merge requires a free block")
	}

	s.Blocks[newDataBlockID].Type = BlockTypeData

	baseLBA := logicalBlockID * s.Config.PagesPerBlock
	for pageOffset := 0; pageOffset < s.Config.PagesPerBlock; pageOffset++ {
		lba := baseLBA + pageOffset
		location, ok := s.Mapping[lba]
		if !ok {
			continue
		}

		page := s.Blocks[location.BlockID].Pages[location.PageOffset]
		if page.State != PageStateValid || page.LBA == nil {
			continue
		}

		s.Blocks[newDataBlockID].Pages[pageOffset] = Page{
			State: PageStateValid,
			LBA:   intPtr(lba),
		}
		s.Mapping[lba] = PhysicalPage{
			BlockID:    newDataBlockID,
			PageOffset: pageOffset,
		}
		s.Metrics.TotalPhysicalWrites++
	}

	if hasOldDataBlock {
		s.eraseBlock(oldDataBlockID)
	}

	s.eraseBlock(logBlockID)
	s.ActiveLogBlockID = nil

	return nil
}

// can the current active log block accept writes for this logical block? (if 1 <-> 1, not FAST)
func (s *Simulator) activeLogBlockMatchesLogicalBlock(blockID int, logicalBlockID int) (bool, error) {
	activeLogicalBlockID, hasLogicalBlock, err := s.logBlockLogicalBlockID(blockID)
	if err != nil {
		return false, err
	}

	if !hasLogicalBlock {
		return true, nil
	}

	return activeLogicalBlockID == logicalBlockID, nil
}

// takes block, checks which logical block they belong to
func (s *Simulator) logBlockLogicalBlockID(blockID int) (int, bool, error) {
	block := s.Blocks[blockID]
	var logicalBlockID int
	hasLogicalBlock := false

	for _, page := range block.Pages {
		if page.LBA == nil {
			continue
		}

		pageLogicalBlockID := s.logicalBlockID(*page.LBA)
		if !hasLogicalBlock {
			logicalBlockID = pageLogicalBlockID
			hasLogicalBlock = true
			continue
		}

		if pageLogicalBlockID != logicalBlockID {
			return 0, false, fmt.Errorf("log block %d spans multiple logical blocks", blockID)
		}
	}

	return logicalBlockID, hasLogicalBlock, nil
}

func (s *Simulator) canSwitchMerge(logBlockID int, logicalBlockID int) bool {
	baseLBA := logicalBlockID * s.Config.PagesPerBlock

	for pageOffset, page := range s.Blocks[logBlockID].Pages {
		expectedLBA := baseLBA + pageOffset
		if page.State != PageStateValid || page.LBA == nil || *page.LBA != expectedLBA {
			return false
		}
	}

	return true
}

func (s *Simulator) findDataBlockForLogicalBlock(logicalBlockID int) (int, bool) {
	for _, block := range s.Blocks {
		if block.Type != BlockTypeData {
			continue
		}

		for _, page := range block.Pages {
			if page.LBA == nil {
				continue
			}

			if s.logicalBlockID(*page.LBA) == logicalBlockID {
				return block.ID, true
			}
		}
	}

	return 0, false
}

func (s *Simulator) findFreeBlock() (int, bool) {
	for _, block := range s.Blocks {
		if block.Type == BlockTypeFree {
			return block.ID, true
		}
	}

	return 0, false
}

func (s *Simulator) nextFreePageOffset(blockID int) int {
	for pageOffset, page := range s.Blocks[blockID].Pages {
		if page.State == PageStateFree {
			return pageOffset
		}
	}

	return -1
}

func (s *Simulator) invalidatePhysicalPage(location PhysicalPage) {
	page := &s.Blocks[location.BlockID].Pages[location.PageOffset]
	if page.State == PageStateValid {
		page.State = PageStateInvalid
	}
}

func (s *Simulator) eraseBlock(blockID int) {
	pages := make([]Page, s.Config.PagesPerBlock)
	for pageOffset := 0; pageOffset < s.Config.PagesPerBlock; pageOffset++ {
		pages[pageOffset] = Page{State: PageStateFree}
	}

	s.Blocks[blockID] = Block{
		ID:    blockID,
		Type:  BlockTypeFree,
		Pages: pages,
	}
	s.Metrics.TotalErases++
}

// lba -> logical block id
func (s *Simulator) logicalBlockID(lba int) int {
	return lba / s.Config.PagesPerBlock
}

func intPtr(v int) *int {
	value := v
	return &value
}
