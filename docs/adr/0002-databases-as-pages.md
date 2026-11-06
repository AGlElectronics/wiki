# ADR 0002 — Databases are pages; rows are child pages

Status: accepted (milestone "Notion-style databases").

## Decision

A database is an ordinary page with `kind = 'database'`. Every row is an
ordinary child page. Three tables carry the rest:

- `database_properties` — the columns: name, type (text, number, select,
  multi_select, date, person, checkbox, url), options (select choices) and
  a float position.
- `page_property_values` — one JSONB value per (row page, property).
- `database_views` — named views: table, board or calendar, with filter,
  sort, group-by and date-property in a JSONB config.

## Why child pages as rows

Everything the wiki already does keeps working with no new code paths:

- **Access.** Rows inherit the database page's access through the normal
  tree walk; sharing, restriction and the 404-not-403 rule apply as-is.
- **Content.** A row opens in the normal live editor (its "page body"),
  with history, comments, files and mentions.
- **Trash.** Deleting a row is the normal page trash; values cascade on
  delete forever.
- **Search and backlinks** index rows like any other page.

## API

- Creating a database = creating a page with `kind: "database"`; a default
  "Table" view is added server-side.
- `GET /api/v1/pages/{id}/database` — properties and views (view access).
- `POST /pages/{id}/properties`, `PATCH|DELETE /properties/{id}` — edit
  access on the database.
- `POST /pages/{id}/views`, `PATCH|DELETE /views/{id}` — edit access.
- `GET /pages/{id}/rows?view=` — the visible child pages with their
  values; the view's filter and sort are applied server-side.
- `PUT /pages/{id}/values` — a row's values (edit access on the row). Each
  property must belong to the row's parent database; values are validated
  by type; null removes a value.

## Trade-offs

- Rows are limited to direct children (no nested databases inside rows'
  sub-pages being rows too). A sub-page of a row is just a page.
- Filters support equality (and contains, for multi_select); sort compares
  JSON scalars. Anything richer stays client-side until needed.
- Formulas, rollups and relations are out of scope for this slice.
