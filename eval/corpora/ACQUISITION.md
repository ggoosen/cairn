# Acquiring corpora — an operator step

**Status: bulk corpus acquisition has NOT been run.** The tooling, the format
and a sample corpus are built; the corpora themselves are not, because the
environment this was built in cannot reach the sources (see "Why this is not
automated" below). Nothing downstream — E4's metrics, E5's task battery, E9's
curves — can start until an operator runs one of the commands here.

Acquisition is deliberately not part of `make eval`. The T0 tier is offline,
deterministic and free; a corpus fetch is none of those, and folding it in
would quietly destroy the property that makes the harness's own tests
trustworthy.

Corpora are **not committed**. They are other people's content under other
people's licences, they are large, and the manifest checksum plus the exact
acquisition command is what makes a result reproducible without shipping the
bytes. (E8 may publish specific corpora deliberately — that is a licensing
decision, made once, not a default.)

---

## 1. GitHub duplicate issues — the strongest signal

A maintainer marking issue #B a duplicate of #A made a relevance judgment
about their own project, with nothing at stake in Cairn.

**Download** (the harness has no fetch path — see "Why there is no fetch
path" below). `gh` handles auth, pagination and rate limits properly:

```sh
REPO=<owner>/<name>
RAW=corpora/raw/github-$(echo "$REPO" | tr / -)
mkdir -p "$RAW"

# closed issues, one file per page
gh api --paginate -H "X-GitHub-Api-Version: 2022-11-28" \
   "repos/$REPO/issues?state=closed&per_page=100" \
   --jq '.' > "$RAW/issues-001.json"

# timelines, for the issues GitHub already flags as duplicates
for n in $(jq -r '.[] | select(.state_reason == "duplicate") | .number' "$RAW"/issues-*.json); do
  gh api -H "X-GitHub-Api-Version: 2022-11-28" \
     "repos/$REPO/issues/$n/timeline?per_page=100" > "$RAW/timeline-$n.json"
done
```

`issues-*.json` must each hold a JSON **array** of issues;
`timeline-<number>.json` an array of that issue's timeline events. (With
`--paginate`, check that your `gh` concatenates into one array; if it emits
several, split them into `issues-001.json`, `issues-002.json`, …)

**Normalize:**

```sh
cairn-eval corpus mine github -repo "$REPO" -raw "$RAW" \
    -out corpora/github-duplicates-$(echo "$REPO" | tr / -)-v1
cairn-eval corpus info corpora/github-duplicates-*-v1
```

**Check the first corpus by hand.** The timeline event's shape has not been
confirmed against the live API here, so the miner reads the canonical issue
from either of the two documented locations and *counts* the events it could
not resolve rather than dropping them silently. Read the manifest's `notes`:
if "did not name a resolvable canonical issue" is large relative to the query
count, the shape assumption is wrong — and the raw responses you kept will
show what the real one is. Normalization is covered by fixture tests; this
one assumption is the part only a real download can settle.

Pick repositories with a duplicate-marking habit and enough volume: large,
well-maintained OSS projects. Note the selection in the corpus's `notes` —
which repositories were chosen is itself a judgment, and it should be visible.

## 2. Stack Overflow duplicates

```sh
# Operator downloads the responses; there is deliberately no fetch path here.
curl -s 'https://api.stackexchange.com/2.3/questions?site=stackoverflow&pagesize=100&filter=<filter-with-body-and-closed_details>' \
    | gunzip > /tmp/so-page1.json

cairn-eval corpus mine stackoverflow -out corpora/stackoverflow-duplicates-v1 /tmp/so-*.json
```

The filter must include `question.body` and `question.closed_details`; the
default filter omits both. The quarterly Stack Exchange data dump works too,
converted to the same `{"items": […]}` envelope.

No fetch path exists on purpose: `api.stackexchange.com` was unreachable
where this tool was built, and shipping HTTP paging that nobody has ever run
is worse than shipping none. Stack Exchange content is CC BY-SA; attribution
travels with it.

## 3. Documentation cross-references — the weakest signal, and offline

```sh
git clone --depth 1 https://github.com/<org>/<docs-repo> /tmp/docs
cairn-eval corpus mine docs \
    -dir /tmp/docs -origin https://github.com/<org>/<docs-repo> \
    -id docs-crossrefs-<org>-v1 -out corpora/docs-crossrefs-<org>-v1
```

A link asserts relevance, not best-answer. It is labelled
`doc_cross_reference` so E4 can weight it separately or exclude it — never
mixed silently with duplicate markers.

Pass `-independent=false` when mining documentation this project wrote. Doing
so downgrades the corpus to a regression gate, which is what it would be.

---

## Why there is no fetch path

Three reasons, and they point the same way.

**It could not be verified.** The environment that built this tooling routes
through an egress proxy: `api.stackexchange.com` returns 403 at the tunnel,
and `api.github.com` serves only repositories the session is scoped to. HTTP
paging that nobody has ever run is the least trustworthy thing that could
have been shipped here, and it would have been the part standing between the
real world and every number downstream.

**It keeps the harness module network-free.** The T0 tier is offline,
deterministic and free (BUILD-PLAN §3.3). With no HTTP client anywhere in
`eval/`, that stops being a habit and becomes structural.

**`gh` and `curl` are better at it.** Auth, pagination, rate limits and
retries are solved problems with mature tools; re-solving them badly inside
an evaluation harness adds a failure mode that would be indistinguishable
from a corpus problem.

So: the format, the normalizers, the loader and a sample corpus are built and
tested; downloading is an operator step with the commands above. Bulk
acquisition **has not been run**, and nothing downstream should pretend
otherwise.
