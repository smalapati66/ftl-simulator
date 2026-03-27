# FTL Simulator Specification (FAST Hybrid Mapping)

## 1. Overview

We are building a simulator for a Flash Translation Layer (FTL) using a FAST-style hybrid mapping scheme.

The simulator models:
- NAND flash blocks and pages
- Logical to physical address mapping
- Log blocks (for writes)
- Data blocks (stable storage)
- Garbage collection via merge operations

We do NOT model:
- Parallelism (channels, dies)
- Real hardware timing (only logical counts)
- Persistent storage

---

## 2. Flash Model

- Total blocks: N (configurable)
- Pages per block: P (configurable)
- Each page stores one LBA

Page states:
- FREE
- VALID
- INVALID

---

## 3. Mapping

We maintain:

- Logical → Physical mapping:
  - Map<LBA, PhysicalPage>

PhysicalPage:
- block_id
- page_offset

---

## 4. Block Types

Each block is one of:
- DATA block
- LOG block
- FREE block

---

## 5. Write Path

Function: write(LBA)

Steps:

1. If LBA already mapped:
   - Mark old physical page as INVALID

2. Find active log block with free page:
   - If none exists → trigger merge

3. Append new page to log block

4. Update mapping:
   - LBA → new physical page

---

## 6. Garbage Collection / Merge

Triggered when:
- No free space in log blocks

We attempt:

### 6.1 Switch Merge (Preferred)

Condition:
- Log block contains all pages of a logical block
- Pages are sequential and complete

Action:
- Promote log block → DATA block
- Erase old DATA block

Cost:
- No page copying

---

### 6.2 Full Merge

Condition:
- Random writes (cannot switch merge)

Steps:

1. Identify corresponding DATA block

2. Allocate new FREE block

3. For each page in logical block:
   - If updated in log block → copy from log block
   - Else → copy from data block

4. Write all pages into new block

5. Update mapping for all LBAs

6. Erase:
   - old DATA block
   - log block

---

## 7. Block Structure

Block:
- id
- type (DATA / LOG / FREE)
- pages[]

Page:
- state (FREE / VALID / INVALID)
- LBA (optional if FREE)

---

## 8. Metrics to Track

- Total logical writes
- Total physical writes
- Total erases
- Write amplification = physical / logical writes

---

## 9. Simplifications

- Single active log block (for V1)
- Greedy merge only
- No wear leveling (initially)

---

## 10. Expected Features

Implement:

- write(LBA)
- read(LBA)
- internal merge()

---

## 11. Notes

- All writes are out-of-place
- Log blocks are append-only
- Mapping must always point to latest VALID page