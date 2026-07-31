# Feature: text preprocessing before embedding

**Status:** proposed

## Summary

Technical books commonly contain title and half-title pages, copyright and
publisher data, tables of contents, indexes, colophons, and publisher
advertising. These sections can produce irrelevant retrieval matches and consume
embedding capacity without improving grounded answers.

RAGLibrarian should conservatively remove confirmed non-answer content before
chunking and embedding. Filtering belongs to Ingestion between extraction and
the stateful chunker:

```text
PDF or EPUB
  -> bounded extraction
  -> content selection
  -> chunking and artifacts
  -> Retrieval embedding and indexing
```

Filtering in Retrieval would be too late: excluded text might already have
crossed chunk overlap boundaries, and changing shard contents there would break
manifest integrity and indexing invariants.

## Content policy

The default policy should be narrow and fail open. Uncertain content remains
available to Retrieval.

Exclude only when classification is sufficiently supported:

- title-only and half-title pages;
- copyright, imprint, publisher, ISBN, and edition boilerplate;
- dedication or blank ornamental pages;
- table of contents, list of figures, and list of tables;
- subject and name indexes;
- publisher catalog, advertising, and "also by" pages;
- end colophon.

Keep by default:

- prefaces, forewords, and introductions;
- all normal chapters and sections;
- appendices;
- glossaries;
- references and bibliographies;
- acknowledgements;
- mixed pages containing both useful text and boilerplate.

A marker word is never sufficient by itself. For example, chapters named
`Indexing Strategies`, prose discussing a table of contents, or an appendix
containing lookup tables must remain indexed.

## Options ranked by difficulty

| Relative difficulty | Option | Approach | Main trade-off |
|---|---|---|---|
| 2/10 | Conservative page heuristics | Combine page position, headings, line density, copyright/ISBN patterns, TOC locator patterns, and index-entry density. | Cheapest and compatible with current extraction, but unusual formatting can evade detection. |
| 4/10 | Format-aware hybrid | Use EPUB structural hints and conservative PDF/EPUB heuristics. | Best cost-to-accuracy fit for the current application. |
| 6/10 | Two-pass document analysis | Classify the complete ordered page sequence before chunking. | More stable PDF decisions, but requires bounded buffering or a temporary extraction artifact. |
| 8/10 | Layout-aware parser | Add a sandboxed structured-document parser such as Docling. | Better hierarchy and layout signals with significant runtime and deployment cost. |
| 9/10 | Trained section classifier | Run a versioned local classifier over position, text, layout, and structural features. | Most adaptable, but requires labeled data, evaluation, monitoring, and model operations. |

### Option 1: conservative page heuristics

Add a document-scoped content selector to the existing extractor-to-chunker
seam. A page or EPUB spine location is excluded only when multiple signals
agree.

Example signals include:

- a sparse page in the front window containing title, author, and publisher
  patterns;
- copyright, ISBN, edition, publisher-address, and "all rights reserved"
  combinations;
- a TOC heading followed by repeated title and page-number pairs or dotted
  leaders;
- a terminal index heading followed by alphabetically ordered short entries
  with high locator-number density;
- terminal catalog-like lists combined with ordering or publisher language.

This is deterministic, inexpensive, and needs no model or external call.
Whole-page classification must keep ambiguous or mixed pages because the
current extractor does not preserve block-level semantics.

### Option 2: format-aware hybrid

Use the conservative rules from option 1 for PDFs. Extend EPUB extraction to
retain bounded structural hints that are currently discarded:

- the EPUB navigation-document identity;
- `epub:type` roles such as `toc`, `index`, `frontmatter`, `bodymatter`, and
  `backmatter`;
- landmarks and their targets;
- spine `linear="no"` hints.

EPUB markup is uploader-controlled and therefore only a classification signal,
not an authorization or trust boundary. Strong semantic metadata plus compatible
position and content signals may justify exclusion; contradictory or ambiguous
signals retain the content.

EPUB defines machine-readable navigation and structural semantics for covers,
front matter, body matter, and indexes. See the
[EPUB 3.3 overview](https://www.w3.org/TR/epub-overview-33/) and
[EPUB structural semantics vocabulary](https://www.w3.org/TR/epub-ssv-11/).

### Option 3: two-pass document analysis

The current extractor streams each page directly into the chunker. Once a chunk
is persisted, later pages cannot safely cause it to be reclassified. A
document-wide algorithm therefore needs a bounded private temporary artifact or
another bounded two-phase representation.

The analysis can then use:

- a front, body, and back section state machine;
- multi-page TOC and index span detection;
- repeated header and footer detection;
- document-relative text and line-density changes;
- evidence accumulated across adjacent pages instead of isolated keyword
  matches.

This is the strongest deterministic approach for PDFs using the current
Poppler-based stack. `pdftotext -layout` preserves approximate physical layout,
but does not supply semantic section roles; see the
[pdftotext documentation](https://manpages.debian.org/bookworm/poppler-utils/pdftotext.1.en.html).

### Option 4: layout-aware parsing

Run Poppler's bounded `-bbox-layout` extraction through the existing parser
sandbox and map page, block, line, word, and bounding-box output into the
Ingestion layout analyzer port. EPUB analysis reuses the existing bounded Go
spine parser. Conservative Go-owned heuristics derive only the labels needed by
the exclusion policy; ambiguous or image-only pages fail open and retain their
content.

This preserves the event-driven worker and versioned result contracts without
introducing another language runtime or model-serving service. It improves
layout signals for born-digital PDFs, but intentionally does not OCR scanned
books. Poppler documents the structured output under `-bbox-layout`; see the
[pdftotext documentation](https://manpages.debian.org/bookworm/poppler-utils/pdftotext.1.en.html).

GROBID can segment title/front/body/bibliography regions, but it is primarily
oriented toward scholarly publications and is a less natural default for a
general technical-book collection. See the
[GROBID full-text model](https://grobid.readthedocs.io/en/latest/training/fulltext/).

### Option 5: trained section classifier

Train a local page or section classifier using features such as:

- relative location in the book;
- text, line, and heading density;
- numeric-locator and alphabetical-entry density;
- heading and boilerplate patterns;
- EPUB structural hints;
- layout labels when available.

A small local classifier is preferable to an external LLM for this task. It is
cheaper, deterministic for a fixed model, easier to resource-bound, and does not
send untrusted book text to another provider. This option should be considered
only after a reviewed, legally usable PDF/EPUB corpus and measurable
false-exclusion targets exist.

## Recommended approach

Implement the format-aware hybrid from option 2 and use a small bounded section
state machine from option 3.

Introduce a document-scoped Ingestion application port with `AddPage` and
`Finish` behavior. The selector:

- preserves original PDF page numbers and EPUB location ordinals for citations;
- emits pages in their original order;
- uses a bounded front window and trailing buffer;
- inserts an explicit semantic boundary around every excluded range so chunk
  overlap cannot bridge it;
- retains content if classification fails or confidence is insufficient;
- prevents excluded headings from changing the chunker's persistent
  chapter/section context.

The first implementation should remain page/location based. Partial-page
removal should wait until extraction preserves trustworthy block structure.

## Compatibility and rollout

- Make filter mode and thresholds validated runtime configuration rather than
  hardcoded operational policy.
- Support disabled, observation, and enforcement modes. Observation records
  decisions without changing artifacts.
- Give the filter a version and policy digest. Include them in the processing
  configuration digest, artifact identity, chunk identity, and supported index
  profile.
- Preserve the original source page/location count; only retained text and
  chunk counts change.
- Reprocess and reindex existing books when an enforced filtering profile is
  introduced. Never silently reuse artifacts produced by another filter
  profile.
- Preserve the original uploaded object so operators can reprocess with
  filtering disabled.
- If filtering would remove all non-empty content, fail with a sanitized
  processing result instead of publishing an empty manifest.
- Finalize all exclusion decisions before any retained page is committed to a
  chunk artifact.
- On ambiguity, classifier errors, or heuristic work/buffer exhaustion, replay
  the complete original extracted document to the chunker without filtering.
  If the selector cannot safely replay the complete stream because its bounded
  temporary representation failed, abort processing atomically with a
  sanitized transient/internal result. Never publish a partially filtered
  manifest or combine already filtered pages with an unfiltered fallback.

## Observability and security

Filtering is a relevance and cost optimization, not a security control.
Retained passages remain untrusted input throughout Retrieval and Answer.

Persist a bounded exclusion-decision record in the manifest or an
integrity-linked private sidecar. It must inherit the chunk artifact's
authorization, retention, and lifecycle deletion and contain:

- filter version and mode;
- filter policy digest;
- fixed decision reason codes;
- original page/location ranges;
- retained and excluded page, byte, or token counts;
- retained/excluded ratios.

This durable record is the audit authority for explaining why source content
was omitted. Information-level diagnostics and metrics may expose bounded
aggregates such as mode, version, reason code, retained/excluded counts and
ratios, and ambiguous-decision counts, but are not a substitute for the durable
record.

Never log excluded or retained text, raw book data, prompts, embeddings, short
text hashes, document-controlled labels, or sidecar identifiers that disclose
private artifact locations. The durable decision record likewise contains no
source text or short-text hashes. Bound classification work, maximum excluded
fraction, and temporary storage. Safety-bound exhaustion follows the atomic
fail-open or abort behavior defined above.

## Acceptance coverage

Use generated, copyright-safe paired PDF and EPUB fixtures with unique
keep/drop sentinels.

Required checks:

- confirmed title, imprint, TOC, index, and advertising sentinels never appear
  in chunk artifacts or Retrieval evidence;
- introductions, normal chapters, misleading headings such as `Indexing
  Strategies`, appendices, glossaries, and references are always retained;
- mixed useful/boilerplate locations remain intact in the page-based version;
- chunk orders remain contiguous and token windows remain valid;
- citations preserve original page/location values;
- no chunk overlap or structure label crosses an excluded range;
- replaying the same source and profile produces identical artifacts;
- changing the filter version or policy changes the processing digest and
  requires reprocessing;
- the integrity-linked exclusion record is deterministic, private, deleted with
  its source artifacts, and contains no source text or short-text hashes;
- ambiguity and classifier failure produce a complete unfiltered artifact,
  while an unrecoverable selector representation failure produces no manifest;
- PDF and EPUB versions of the same synthetic structure make equivalent
  keep/drop decisions;
- ambiguous markup, Unicode variants, OCR noise, misleading keywords, and
  uploader-controlled EPUB roles fail open;
- unit and race tests prove classification bounds and determinism, followed by
  isolated ingestion and retrieval integration tests.

Before enforcement becomes the default, a reviewed representative corpus must
show zero loss for protected content classes and acceptable excluded-content
contamination. Observation data must contain only sanitized aggregate metadata.
