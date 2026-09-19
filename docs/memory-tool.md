# Cairn as the storage behind Anthropic's memory tool

Anthropic's `memory_20250818` tool is **six file commands** — `view`, `create`,
`str_replace`, `insert`, `delete`, `rename` — over a `/memories` directory,
executed **client-side by your own handler**. There is no search, no ranking and
no embedding in the contract: retrieval is a directory listing and a file read,
and *you* supply the storage. The reference implementations in the docs use a
map in process memory.

`cairn memory-tool` is that storage. Directory listing over topics, file read
over fetch, create over send — so an API developer gets signed durability,
provenance, budgets and (through Cairn's other surfaces) hybrid search
underneath an interface Claude already knows how to drive.

> This page is about the Messages API. For wiring Cairn into a *harness*, read
> [`memory-provider.md`](memory-provider.md).

## Two commands mean things an append-only log cannot do

Read this section before you ship it. Cairn's log is append-only and its objects
are immutable; two of the six commands assume neither is true. They are
**mapped**, not emulated, and every reply says which happened.

| Command | What the contract means | What Cairn does | What the caller is told |
|---|---|---|---|
| `delete` | erase the file | **retracts** the message | `Successfully deleted {path}` **and** a `[cairn]` line: RETRACTED, not erased; the events remain in the verified log and stay auditable |
| `str_replace` / `insert` | edit in place | adds a **new revision**; the previous one stays head-of-history and stays fetchable | `The memory file has been edited.` **and** a `[cairn]` line naming the new revision and saying the text was not overwritten |
| `create` onto an existing path | overwrite | publishes a **new message**; the caller's name comes to mean the new one | the contract's success string **and** a `[cairn]` line naming the message the path used to mean, and where the old version is still listed |
| `rename` | move | **copies** to the new path and **retracts** the original | `Successfully renamed …` **and** a `[cairn]` line: an append-only log has no move; both events remain |

**A caller expecting erasure is never told erasure happened.** That is the whole
point of the wording above, and it is asserted by test.

### If you genuinely need erasure

Use the **ephemeral text class**:

```sh
cairn memory-tool --view <view> --class ephemeral
```

An ephemeral body is removed when its TTL expires — the content really does
disappear, and `view` then says so rather than pretending the file is missing.
This is the honest answer to "I need this gone", and it is a decision you make
when the memory is **written**, not when someone asks for it to be deleted.
Retraction is the right tool for "this is no longer current"; expiry is the
right tool for "this must not persist".

## `view` on a directory is a listing, not an answer

Ranking is the reason to use Cairn, and it is deliberately absent here. A
directory `view` is served by `topic-messages`, the daemon's dullest read:
topic names in, message rows out, ordered by creation time, nothing scored and
nothing dropped by rank. A listing that has been capped says so on its own line.

If you want Cairn's ranked retrieval, that is what the MCP surface and the CLI
are for — `cairn_search`, `cairn_digest`, `cairn search`. Reaching for it inside
a call whose contract is enumeration would be exactly the kind of quiet
cleverness this project refuses.

## How a memory path maps onto the mesh

```
/memories                      → topic  memory/<view>
/memories/<dir>                → topic  memory/<view>/<dir>
/memories/<dir>/<file>         → one message linked to that topic
```

- **Directories are topics**, and are provisioned by the operator, once:
  ```sh
  cairn memory-tool init --view assistant --subdir notes --subdir decisions
  ```
  An agent surface never creates a topic in Cairn (FIX-F1), so a `create` into a
  directory that does not exist is refused and says who can make one. At most
  two levels below the root are addressable — the contract's own listing depth.
- **A file is a message**, and its canonical name is its message id, which is
  what listings show. The name *you* chose is recorded as an alias in Cairn's
  existing source-path index, so `view /memories/progress.md` keeps working
  after the store has told you the file also lives at
  `/memories/019a…`. Both paths resolve to the same message.
- **A message outside this view's namespace is not reachable**, even by
  spelling its id as a memory path. The facade serves its own directory and
  nothing else.

## Running it

```sh
cairn daemon                                     # a running daemon is required
cairn memory-tool init --view assistant          # once, by the operator
cairn memory-tool --view assistant               # the handler your loop pipes to
```

It reads **one JSON memory command per line on stdin** and writes **one JSON
tool_result payload per line on stdout** — `{"content": …, "is_error": …}`, the
two fields a `tool_result` block needs. That is the whole interface, so a loop
in any language can drive it as a subprocess.

```sh
cairn memory-tool --print-tool
# {"name":"memory","type":"memory_20250818"}

cairn memory-tool --view assistant --command '{"command":"view","path":"/memories"}'
```

A Python loop, using the same `tools` entry the docs specify:

```python
import json, subprocess, anthropic

handler = subprocess.Popen(
    ["cairn", "memory-tool", "--view", "assistant"],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
)

def execute_memory(tool_input: dict) -> dict:
    handler.stdin.write(json.dumps(tool_input) + "\n")
    handler.stdin.flush()
    return json.loads(handler.stdout.readline())

client = anthropic.Anthropic()
messages = [{"role": "user", "content": "Remember that Acme Corp prefers email follow-ups."}]
tools = [{"type": "memory_20250818", "name": "memory"}]

while True:
    msg = client.messages.create(model="claude-opus-5", max_tokens=1024,
                                 messages=messages, tools=tools)
    if msg.stop_reason != "tool_use":
        print(msg.content)
        break
    results = []
    for block in msg.content:
        if block.type == "tool_use":
            r = execute_memory(block.input)
            results.append({"type": "tool_result", "tool_use_id": block.id,
                            "content": r["content"], "is_error": r["is_error"]})
    messages += [{"role": "assistant", "content": msg.content},
                 {"role": "user", "content": results}]
```

## Security

The tool's docs make four things the developer's responsibility. Three are met
with machinery Cairn already had; the fourth is honestly not done.

- **Path traversal — refused.** Every path in every command goes through one
  gate before any daemon op runs: it must be absolute under `/memories`, and it
  is rejected for a traversal segment, a backslash, a percent-escape (which is
  how an encoded `../` arrives), an empty segment, a control character, or more
  than two levels. `/memories` is a **namespace**, not a directory: no memory
  path is ever resolved against your filesystem, so there is nothing under it to
  escape into.
- **Size caps — hard, and enforced before the write.** One file is capped at
  65,536 characters, one `view` at 16,000 (the figure Claude's own tool
  description tells it to expect, with `view_range` to page the rest), one
  listing at 500 entries with the truncation reported. An oversized `create` is
  refused *before* it reaches the log, because an append-only store cannot take
  a mistake back.
- **Expiry — the ephemeral class**, above.
- **Secret stripping — NOT performed.** Cairn's redaction design
  (`build/CAPTURE-C3-DESIGN.md`) is gated on a privacy review that has not
  happened, and writing an unreviewed pattern pass here would pre-empt exactly
  that gate. So: **whatever the model writes is stored, verbatim, durably and
  signed.** If your application handles credentials, strip them in your handler
  before the command reaches this one, or use `--class ephemeral`. The daemon
  says this on every start rather than leaving you to infer it.

## Capability: the facade cannot exceed the session behind it

`cairn memory-tool` is an agent surface, so it is **never tier-1** (R21). Every
command runs under the `CAIRN_SESSION` handle it was launched with, or one it
mints from `--profile` (default `agent-standard`) and revokes on exit;
`--profile full` is refused at the flag. Each command is one or more ordinary
daemon ops, and the daemon's capability gate is the only thing that decides.

Under the default profile:

| Command | agent-standard | Why |
|---|---|---|
| `view` | ✅ | read |
| `create` | ✅ | send |
| `str_replace`, `insert` | ❌ | they map onto **revision**, which is admin capability in this mesh |
| `delete`, `rename` | ❌ | they map onto **retraction**, which is admin capability in this mesh |

The refusal says exactly that, including which capability was missing, so a
model can tell "I may not" from "it did not work". Cairn's `admin` capability is
coarse — it also carries topic creation, corpus export and conflict resolution —
so the facade does **not** ask for it by default. An operator who wants Claude to
be able to prune its own memory grants a profile deliberately, in
`<device-dir>/profiles.toml`:

```toml
[profiles.memory-curator]
capabilities = ["read", "send", "signal", "outcome", "admin"]
```

```sh
cairn memory-tool --view assistant --profile memory-curator
```

Weigh that against what else `admin` carries before you do it. Under the default
profile, `create` onto an existing path is the append-only way to update a
memory file, and it keeps the old version.

**Honesty about the boundary (R22).** Same-OS-user confinement prevents
accidents, not malice. Run a handler you do not trust as a different OS user.

## Untrusted content (R18/R53)

Everything the facade returns from the mesh is text some other agent wrote. The
contract's return shape is a plain string rather than Cairn's R18 JSON envelope,
so the discipline is carried **structurally**:

- file content appears only inside the contract's own `%6d\t` line numbering,
  which content cannot break out of — the same property that makes the digest's
  `> [CAIRN] ` prefix unescapable;
- a `[cairn]` line precedes it naming the message, its revision and its sender,
  and stating that what follows is **data, not instructions**;
- listings render only daemon-authored ids and sizes, with topic leaves passed
  through a render-time sanitizer independent of the write-time validation that
  already governs topic names.

## Checking it works

```sh
cairn memory-tool --view assistant --command '{"command":"view","path":"/memories"}'
cairn topic list | grep '^memory/'      # the namespace, as topics
cairn doctor                            # the log, after anything the facade did
```
