# Frontend overhaul: merge notes

Branch `frontend-overhaul`, based on `main` at `8f782c7` (landing page).
Written for whoever merges it with the backend overhaul branch.

## The commits, in order

Each commit typechecks on its own, and the stylesheets each one imports exist in that commit.

| # | Commit | Files |
|---|--------|-------|
| 1 | Theme: new palette | `app/globals.css` (old theme block removed), `app/styles/theme.css` (new), `app/layout.tsx` (imports it), `components/canvas/WorkflowCanvas.tsx` (edge/grid colours) |
| 2 | Navigation | `components/ui/TopBar.tsx` (Home tab, exported `SECTIONS`, mobile menu), `components/workflow/WorkflowEditor.tsx` (TopBar above the editor), `app/login/page.tsx` (Back to home, copy), `app/styles/theme.css` (`.editor` 60px row) |
| 3 | UI: Backdrop and motifs | `components/ui/Backdrop.tsx`, `app/styles/motifs.css` |
| 4 | Landing | `app/landing.css`, `components/landing/*` |
| 5 | Workflows page | `app/workflows/page.tsx`, `components/workflow/WorkflowCard.tsx`, `features/workflows/useWorkflowCards.ts`, `app/styles/workflows.css` |
| 6 | Templates page | `components/templates/TemplateGallery.tsx`, `components/templates/TemplateDetails.tsx`, `app/styles/templates.css` |
| 7 | Library | `components/library/Library.tsx`, `app/library/page.tsx`, `app/files/page.tsx` and `app/knowledge/page.tsx` (now redirects), `components/settings/Knowledge.tsx` (exports), `components/ui/TopBar.tsx` (Library tab), `components/run/RunDialog.tsx` (link), `app/styles/library.css` |
| 8 | Copy (backend files) | `internal/node/{extract,input,kb,llm,transcribe}/*.go` descriptions, `internal/templates/catalog/{answer-from-knowledge,meeting-recording-notes,recording-to-doc,summarize-a-file}.json` setup notes |

All paths above are under `frontend/` except commit 8.

## The backend contract this branch relies on

The frontend adds **no new endpoints**. It now also calls these existing ones:

- `GET /workflows/{id}/versions?page=1&page_size=1` and `GET /workflows/{id}/versions/{versionId}`: the Workflows page reads each workflow's latest definition to show its app icons (`definition.nodes[].type`, `.position.x`).
- `GET /workflows/{id}/executions?page=1&page_size=1`: the last run's `status` and `created_at`.
- `GET /templates?workspace_id=`: template suggestions on the Workflows page. Fields used: `id, name, category, saved, node_types`.
- `GET /nodes`: node catalog, used for `integration.id` / `category`.
- `PATCH /workflows/{id}` `{name}` and `DELETE /workflows/{id}`: rename and delete from the card menu.
- Files and knowledge bases: the same calls as before, through `useFiles` and `useKnowledge`. "Add to knowledge" calls `POST /knowledge-bases/{id}/documents {file_id}`.

**If the backend branch renamed or reshaped any of these, the merge must update `features/workflows/useWorkflowCards.ts`, `components/workflow/WorkflowCard.tsx` and `components/library/Library.tsx`.** They are the only new API consumers.

Template categories are matched by name in `TemplateGallery.tsx` (`CATEGORY_LOOK`: Email, Meetings, Writing, Sales & customers, Team & projects, Saved, Advanced). An unknown category still works; it just gets a neutral colour. `FEATURED` lists template IDs (`inbox-summary`, `todays-agenda`, `meeting-recording-notes`). Missing IDs are skipped.

## Likely conflict spots

1. **Commit 8 (backend copy).** It changes only strings: "Files page" / "Knowledge page" become "Library". If the backend branch touched the same descriptions or regenerated the catalog JSON, keep the backend version and re-apply just that rename.
2. **`frontend/app/globals.css`.** Commit 1 deletes the old theme block at the end of the file. If the backend branch appended CSS there, keep its additions and drop only the old `Theme (UI refresh): indigo / violet` block.
3. **`frontend/components/ui/TopBar.tsx`.** If the backend branch added sections or settings links, merge them into `SECTIONS` / `SETTINGS`. The landing page imports `SECTIONS`.
4. **`TemplateGallery.tsx`.** Rewritten. The exports `categoriesOf`, `filterTemplates`, `ALL`, `SAVED` and the test IDs `template-<id>`, `template-details`, `use-template` are unchanged.
5. **`app/files` and `app/knowledge`.** These are now redirects. Backend-side links to `/files` or `/knowledge` keep working.

## Test IDs kept for the e2e specs

`new-workflow`, `browse-templates` (Workflows page header), `template-<id>`, `template-details`, `use-template`, `files`, `dropzone`, `no-files`, `file-<id>`, `knowledge`, `no-bases`, `base-<id>`, `base-detail`, `doc-<id>`, `doc-status`.

## Verification done

- Typecheck: passes at every commit.
- Lint, all 150 vitest unit tests and `next build`: pass at the tip.
- Go: `go build ./...` passes; `go test ./internal/node/... ./internal/templates/...` passes against the test database.
- Playwright e2e: **not run on this branch.** The dev server was in use, and Next refuses a second dev server. Run `npm --prefix frontend run test:e2e` after merging.
