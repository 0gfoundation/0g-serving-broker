---
name: evermemos
description: Search and store memories using EverMemOS. Use when user asks about past conversations, previous decisions, or when important information should be remembered. Also use to recall project context and historical knowledge.
argument-hint: "[search|store|recent] [query/content]"
allowed-tools: Bash(python3 *)
---

# EverMemOS Memory Integration

## Commands

### Search
```
python3 "$HOME/.claude/skills/evermemos/scripts/evermemos_client.py" search "<query>" [method] [top_k]
```
- `method`: `keyword` | `vector` | `hybrid` (default) | `rrf` | `agentic`
- `top_k`: max results (default: 5)

### Store
```
python3 "$HOME/.claude/skills/evermemos/scripts/evermemos_client.py" store "<content>" [user|assistant]
```

### Recent
```
python3 "$HOME/.claude/skills/evermemos/scripts/evermemos_client.py" recent [limit]
```

---

## When to Use (Automatic Triggers)

**Search** when:
- User references past work: "remember when…", "last time", "what did we discuss about X"
- Before implementing a feature or debugging — check for prior context
- A specific module or component is mentioned that may have history

**Store** when:
- User says "remember this" or "make a note"
- An important decision, bug fix, or pattern is established
- A project milestone is reached

**Recent** when:
- User asks "what were we working on?" or returns after a break

> When in doubt, search. A missed context costs hours; an unnecessary search costs seconds.

---

## Configuration

```bash
export EVERMEMOS_BASE_URL="http://localhost:1995"
export EVERMEMOS_USER_ID="claude_code_user"
# group_id is auto-derived from working directory
```

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| Connection error | Verify `curl $EVERMEMOS_BASE_URL` responds |
| No results | Try different keywords, switch to `vector` or increase `top_k` |
| Permission error | `chmod +x ~/.claude/skills/evermemos/scripts/evermemos_client.py` |
