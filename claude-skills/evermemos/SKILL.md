---
name: evermemos
description: Search memories using EverMemOS. PROACTIVELY search before answering ANY project-related questions. Use when user asks about past conversations, previous decisions. ALWAYS check history before implementing features, debugging issues, or suggesting solutions. Maintain project continuity across sessions. Always put search results into your context window to improve your response.
argument-hint: search <query> [method] [top_k]
allowed-tools: Bash(python3 *)
---

# EverMemOS Memory Integration

## Commands

The script to invoke is always:
`~/.claude/skills/evermemos/scripts/evermemos_client.py`

### search

```
/evermemos search <query> [method] [top_k]
```

- `method`: `hybrid` (default), `agentic`
- `top_k`: max results (default: 10)

**When to use — ALWAYS trigger when:**
- User asks "What did we discuss about X?" / "Did we fix that bug?" / "How did we solve Y?"
- Any question containing: "last time", "before", "previously", "remember", "earlier"
- Before implementing features — check for prior decisions, patterns, gotchas
- Before debugging — check if this issue was seen before
- User mentions specific modules, files, or components — search for prior context
- User references a decision or approach without full explanation

```bash
python3 "$HOME/.claude/skills/evermemos/scripts/evermemos_client.py" search "<query>"
```

**Agentic search example** (for complex or vague queries):
```bash
python3 "$HOME/.claude/skills/evermemos/scripts/evermemos_client.py" search "authentication flow" agentic
```

---

## Proactive Usage Rules

**Rule 1 — Search first, answer second:**

```
User asks a question
       ↓
Is it about THIS project?
   YES → SEARCH FIRST, then answer with context
   NO  → Answer directly
```

> When in doubt, search. Missing context costs hours; an unnecessary search costs seconds.

**Rule 2 — Multi-angle search for complex questions:**

Don't stop at one query. Search from multiple angles to surface all relevant history:

```
Topic: authentication implementation
Search 1: "authentication flow"
Search 2: "JWT token handling"
Search 3: "login session management"
```

Combine results before answering. Prior decisions may be stored under different terminology.

**Rule 3 — Never search and ignore results:**

After retrieving memories, always incorporate them into your response or reasoning. If results are irrelevant, say so explicitly — don't silently discard them. Search results are context for improving accuracy, not noise to filter.

---

## Retrieval Methods

**Default**: `hybrid` — combines keyword and vector search. Use for all standard queries.

**Only use `agentic` when:**
- Query is vague or ambiguous
- `hybrid` returned nothing useful
- User explicitly asks for a deep or thorough memory search

---

## Troubleshooting

**Connection error**: Verify EverMemOS is running. Check with `curl http://localhost:1995` — if it doesn't respond, start the EverMemOS service.

**No results**: Try different keywords or switch to `agentic` method. Increase `top_k` (e.g., `top_k=20`). The memory may be stored under different terminology.
