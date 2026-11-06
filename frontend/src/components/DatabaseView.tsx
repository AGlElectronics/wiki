// Notion-style database on a database page (ADR 0002): named views over
// the child pages. Table edits values inline, Board drags cards between
// the groups of a select column, Calendar lays rows on a month by a date
// column. A row opens as a normal page.
import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { CalendarDays, Columns3, MoreHorizontal, Plus, Table2, Trash2 } from 'lucide-react';
import {
  createPage,
  createProperty,
  createView,
  deleteProperty,
  deleteView,
  getDatabase,
  listRows,
  searchPeople,
  setRowValues,
  updateProperty,
  updateView,
  type DBProperty,
  type DBRow,
  type DBView,
  type Page,
  type PropertyType,
} from '@/api/wiki';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Dialog';
import { LoadingRow } from '@/components/ui/States';
import { cn } from '@/lib/utils';

const PROPERTY_TYPES: { value: PropertyType; label: string }[] = [
  { value: 'text', label: 'Text' },
  { value: 'number', label: 'Number' },
  { value: 'select', label: 'Select' },
  { value: 'multi_select', label: 'Multi-select' },
  { value: 'date', label: 'Date' },
  { value: 'person', label: 'Person' },
  { value: 'checkbox', label: 'Checkbox' },
  { value: 'url', label: 'URL' },
];

const VIEW_ICONS = { table: Table2, board: Columns3, calendar: CalendarDays } as const;

function usePeopleOptions(enabled: boolean) {
  const people = useQuery({
    queryKey: ['people', ''],
    queryFn: ({ signal }) => searchPeople('', signal),
    enabled,
    staleTime: 60_000,
  });
  return people.data ?? [];
}

// ---------------------------------------------------------------------------
// Cells
// ---------------------------------------------------------------------------

function Cell({
  row,
  prop,
  canEdit,
  onSet,
}: {
  row: DBRow;
  prop: DBProperty;
  canEdit: boolean;
  onSet: (value: unknown) => void;
}) {
  const value = row.values[prop.id];
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');
  const people = usePeopleOptions(prop.type === 'person');
  const choices = prop.options?.choices ?? [];

  if (prop.type === 'checkbox') {
    return (
      <input
        type="checkbox"
        checked={value === true}
        disabled={!canEdit}
        aria-label={prop.name}
        onChange={(e) => onSet(e.target.checked)}
      />
    );
  }
  if (prop.type === 'select') {
    return (
      <Select
        value={typeof value === 'string' ? value : ''}
        disabled={!canEdit}
        aria-label={prop.name}
        className="h-7 text-xs w-full"
        onChange={(e) => onSet(e.target.value === '' ? null : e.target.value)}
      >
        <option value="">—</option>
        {choices.map((ch) => (
          <option key={ch} value={ch}>
            {ch}
          </option>
        ))}
      </Select>
    );
  }
  if (prop.type === 'multi_select') {
    const selected = Array.isArray(value) ? (value as string[]) : [];
    return (
      <div className="flex flex-wrap gap-1">
        {choices.map((ch) => {
          const on = selected.includes(ch);
          return (
            <button
              key={ch}
              type="button"
              disabled={!canEdit}
              className={cn(
                'rounded-pill px-2 py-0.5 text-[11px] border',
                on ? 'bg-primary-soft border-primary text-primary' : 'border-border text-muted-foreground'
              )}
              onClick={() => onSet(on ? selected.filter((s) => s !== ch) : [...selected, ch])}
            >
              {ch}
            </button>
          );
        })}
      </div>
    );
  }
  if (prop.type === 'person') {
    const id = typeof value === 'string' ? value : '';
    return (
      <Select
        value={id}
        disabled={!canEdit}
        aria-label={prop.name}
        className="h-7 text-xs w-full"
        onChange={(e) => onSet(e.target.value === '' ? null : e.target.value)}
      >
        <option value="">—</option>
        {people.map((p) => (
          <option key={p.id} value={p.id}>
            {p.display_name}
          </option>
        ))}
      </Select>
    );
  }
  if (prop.type === 'date') {
    return (
      <input
        type="date"
        value={typeof value === 'string' ? value : ''}
        disabled={!canEdit}
        aria-label={prop.name}
        className="h-7 w-full bg-transparent text-xs outline-none"
        onChange={(e) => onSet(e.target.value === '' ? null : e.target.value)}
      />
    );
  }
  // text, number, url: click to edit, commit on blur or Enter.
  if (!editing) {
    const shown = value === undefined || value === null ? '' : String(value);
    return (
      <button
        type="button"
        disabled={!canEdit}
        className="w-full min-h-7 text-left text-xs truncate disabled:cursor-default"
        onClick={() => {
          setDraft(shown);
          setEditing(true);
        }}
      >
        {prop.type === 'url' && shown ? (
          <a href={shown} target="_blank" rel="noreferrer" className="text-primary underline" onClick={(e) => e.stopPropagation()}>
            {shown}
          </a>
        ) : (
          shown || <span className="text-muted-foreground">—</span>
        )}
      </button>
    );
  }
  const commit = () => {
    setEditing(false);
    const trimmed = draft.trim();
    if (trimmed === '') {
      onSet(null);
    } else if (prop.type === 'number') {
      const n = Number(trimmed);
      if (!Number.isNaN(n)) onSet(n);
    } else {
      onSet(trimmed);
    }
  };
  return (
    <Input
      autoFocus
      value={draft}
      aria-label={prop.name}
      className="h-7 text-xs"
      type={prop.type === 'number' ? 'number' : 'text'}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') commit();
        if (e.key === 'Escape') setEditing(false);
      }}
    />
  );
}

// ---------------------------------------------------------------------------
// Table view
// ---------------------------------------------------------------------------

function ColumnHeader({
  prop,
  canEdit,
  view,
  onSort,
}: {
  prop: DBProperty;
  canEdit: boolean;
  view: DBView;
  onSort: () => void;
}) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const rename = useMutation({
    mutationFn: (name: string) => updateProperty(prop.id, { name }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['database', prop.database_id] }),
  });
  const remove = useMutation({
    mutationFn: () => deleteProperty(prop.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['database', prop.database_id] });
      void qc.invalidateQueries({ queryKey: ['rows', prop.database_id] });
    },
  });
  const sort = view.config.sort?.[0];
  const sortMark = sort?.property === prop.id ? (sort.desc ? ' ↓' : ' ↑') : '';
  return (
    <th className="relative border-b border-border px-2 py-1.5 text-left text-xs font-semibold whitespace-nowrap">
      <button type="button" className="hover:text-primary" onClick={onSort} title="Sort by this column">
        {prop.name}
        {sortMark}
      </button>
      {canEdit ? (
        <button
          type="button"
          aria-label={`Column menu for ${prop.name}`}
          className="ml-1 opacity-40 hover:opacity-100"
          onClick={() => setOpen(!open)}
        >
          <MoreHorizontal className="inline w-3 h-3" />
        </button>
      ) : null}
      {open ? (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute left-0 z-50 mt-1 w-44 rounded-md border border-border bg-card py-1 shadow-pop font-normal">
            <button
              type="button"
              className="w-full px-3 py-1.5 text-left text-xs hover:bg-secondary"
              onClick={() => {
                const name = window.prompt('Column name', prop.name);
                setOpen(false);
                if (name?.trim()) rename.mutate(name.trim());
              }}
            >
              Rename
            </button>
            {prop.type === 'select' || prop.type === 'multi_select' ? (
              <button
                type="button"
                className="w-full px-3 py-1.5 text-left text-xs hover:bg-secondary"
                onClick={() => {
                  const raw = window.prompt(
                    'Choices, comma separated',
                    (prop.options?.choices ?? []).join(', ')
                  );
                  setOpen(false);
                  if (raw === null) return;
                  const choices = raw.split(',').map((s) => s.trim()).filter(Boolean);
                  void updateProperty(prop.id, { options: { choices } }).then(() =>
                    qc.invalidateQueries({ queryKey: ['database', prop.database_id] })
                  );
                }}
              >
                Edit choices
              </button>
            ) : null}
            <button
              type="button"
              className="w-full px-3 py-1.5 text-left text-xs text-destructive hover:bg-secondary"
              onClick={() => {
                setOpen(false);
                remove.mutate();
              }}
            >
              Delete column
            </button>
          </div>
        </>
      ) : null}
    </th>
  );
}

function TableView({
  dbID,
  properties,
  rows,
  view,
  canEdit,
  onSetValue,
}: {
  dbID: string;
  properties: DBProperty[];
  rows: DBRow[];
  view: DBView;
  canEdit: boolean;
  onSetValue: (rowID: string, propID: string, value: unknown) => void;
}) {
  const qc = useQueryClient();
  const toggleSort = (propID: string) => {
    const current = view.config.sort?.[0];
    const desc = current?.property === propID ? !current.desc : false;
    void updateView(view.id, { config: { ...view.config, sort: [{ property: propID, desc }] } }).then(() => {
      void qc.invalidateQueries({ queryKey: ['database', dbID] });
      void qc.invalidateQueries({ queryKey: ['rows', dbID] });
    });
  };
  return (
    <div className="overflow-x-auto rounded-card border border-border">
      <table className="w-full text-sm">
        <thead>
          <tr>
            <th className="border-b border-border px-2 py-1.5 text-left text-xs font-semibold min-w-48">Name</th>
            {properties.map((p) => (
              <ColumnHeader key={p.id} prop={p} canEdit={canEdit} view={view} onSort={() => toggleSort(p.id)} />
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.id} className="border-b border-border last:border-b-0 hover:bg-secondary/40">
              <td className="px-2 py-1">
                <Link
                  to="/p/$pageId"
                  params={{ pageId: row.id }}
                  className="flex items-center gap-1.5 text-xs font-medium hover:underline"
                >
                  <span>{row.icon || '📄'}</span>
                  <span className="truncate">{row.title.trim() || 'Untitled'}</span>
                </Link>
              </td>
              {properties.map((p) => (
                <td key={p.id} className="px-2 py-1 min-w-32">
                  <Cell
                    row={row}
                    prop={p}
                    canEdit={canEdit && row.access !== 'view' && row.access !== 'comment'}
                    onSet={(v) => onSetValue(row.id, p.id, v)}
                  />
                </td>
              ))}
            </tr>
          ))}
          {rows.length === 0 ? (
            <tr>
              <td colSpan={properties.length + 1} className="px-2 py-6 text-center text-sm text-muted-foreground">
                No rows yet.
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Board view
// ---------------------------------------------------------------------------

function BoardView({
  properties,
  rows,
  view,
  canEdit,
  onSetValue,
  onConfig,
}: {
  properties: DBProperty[];
  rows: DBRow[];
  view: DBView;
  canEdit: boolean;
  onSetValue: (rowID: string, propID: string, value: unknown) => void;
  onConfig: (config: DBView['config']) => void;
}) {
  const selects = properties.filter((p) => p.type === 'select');
  const groupProp = selects.find((p) => p.id === view.config.group_by) ?? selects[0];
  if (!groupProp) {
    return <p className="text-sm text-muted-foreground">Add a select column to group the board by.</p>;
  }
  const groups = [...(groupProp.options?.choices ?? []), null];
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        Grouped by
        <Select
          value={groupProp.id}
          className="h-7 text-xs"
          aria-label="Group by"
          onChange={(e) => onConfig({ ...view.config, group_by: e.target.value })}
        >
          {selects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </Select>
      </div>
      <div className="flex gap-3 overflow-x-auto pb-2">
        {groups.map((group) => {
          const inGroup = rows.filter((r) => {
            const v = r.values[groupProp.id];
            return group === null ? v === undefined || v === null || v === '' : v === group;
          });
          return (
            <div
              key={group ?? '__none'}
              className="w-60 shrink-0 rounded-card border border-border bg-card/50 p-2"
              onDragOver={(e) => {
                if (canEdit) e.preventDefault();
              }}
              onDrop={(e) => {
                if (!canEdit) return;
                e.preventDefault();
                const rowID = e.dataTransfer.getData('text/wiki-row');
                if (rowID) onSetValue(rowID, groupProp.id, group);
              }}
            >
              <div className="px-1 pb-2 text-xs font-semibold text-muted-foreground">
                {group ?? 'No value'} <span className="font-normal">({inGroup.length})</span>
              </div>
              <div className="space-y-1.5">
                {inGroup.map((r) => (
                  <Link
                    key={r.id}
                    to="/p/$pageId"
                    params={{ pageId: r.id }}
                    draggable={canEdit}
                    onDragStart={(e) => e.dataTransfer.setData('text/wiki-row', r.id)}
                    className="block rounded-md border border-border bg-card px-2.5 py-2 text-xs font-medium hover:border-primary"
                  >
                    {r.icon ? `${r.icon} ` : ''}
                    {r.title.trim() || 'Untitled'}
                  </Link>
                ))}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Calendar view
// ---------------------------------------------------------------------------

function CalendarView({
  properties,
  rows,
  view,
  onConfig,
}: {
  properties: DBProperty[];
  rows: DBRow[];
  view: DBView;
  onConfig: (config: DBView['config']) => void;
}) {
  const [month, setMonth] = useState(() => {
    const now = new Date();
    return new Date(now.getFullYear(), now.getMonth(), 1);
  });
  const dates = properties.filter((p) => p.type === 'date');
  const dateProp = dates.find((p) => p.id === view.config.date_property) ?? dates[0];
  if (!dateProp) {
    return <p className="text-sm text-muted-foreground">Add a date column to place rows on the calendar.</p>;
  }
  const first = new Date(month.getFullYear(), month.getMonth(), 1);
  // Monday-first grid.
  const lead = (first.getDay() + 6) % 7;
  const start = new Date(first);
  start.setDate(first.getDate() - lead);
  const days = Array.from({ length: 42 }, (_, i) => {
    const d = new Date(start);
    d.setDate(start.getDate() + i);
    return d;
  });
  const iso = (d: Date) =>
    `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
  const byDay = new Map<string, DBRow[]>();
  for (const r of rows) {
    const v = r.values[dateProp.id];
    if (typeof v === 'string' && v) {
      const key = v.slice(0, 10);
      byDay.set(key, [...(byDay.get(key) ?? []), r]);
    }
  }
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-sm">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}
        >
          ←
        </Button>
        <span className="font-semibold w-36 text-center">
          {month.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })}
        </span>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}
        >
          →
        </Button>
        <div className="flex-1" />
        <Select
          value={dateProp.id}
          className="h-7 text-xs"
          aria-label="Date column"
          onChange={(e) => onConfig({ ...view.config, date_property: e.target.value })}
        >
          {dates.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </Select>
      </div>
      <div className="grid grid-cols-7 gap-px rounded-card border border-border bg-border overflow-hidden text-xs">
        {['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'].map((d) => (
          <div key={d} className="bg-card px-2 py-1 font-semibold text-muted-foreground">
            {d}
          </div>
        ))}
        {days.map((d) => {
          const inMonth = d.getMonth() === month.getMonth();
          const items = byDay.get(iso(d)) ?? [];
          return (
            <div key={iso(d)} className={cn('bg-card min-h-20 px-1.5 py-1', !inMonth && 'opacity-40')}>
              <div className="text-[10px] text-muted-foreground">{d.getDate()}</div>
              {items.map((r) => (
                <Link
                  key={r.id}
                  to="/p/$pageId"
                  params={{ pageId: r.id }}
                  className="block truncate rounded bg-primary-soft px-1 py-0.5 mb-0.5 text-[11px] text-primary hover:underline"
                >
                  {r.title.trim() || 'Untitled'}
                </Link>
              ))}
            </div>
          );
        })}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// The database section itself
// ---------------------------------------------------------------------------

export function DatabaseView({ page, canEdit }: { page: Page; canEdit: boolean }) {
  const qc = useQueryClient();
  const [viewID, setViewID] = useState<string | null>(null);
  const [addingProp, setAddingProp] = useState(false);
  const [propName, setPropName] = useState('');
  const [propType, setPropType] = useState<PropertyType>('text');

  const schema = useQuery({
    queryKey: ['database', page.id],
    queryFn: ({ signal }) => getDatabase(page.id, signal),
  });
  const views = schema.data?.views ?? [];
  const view = views.find((v) => v.id === viewID) ?? views[0];
  const rows = useQuery({
    queryKey: ['rows', page.id, view?.id],
    queryFn: ({ signal }) => listRows(page.id, view?.id, signal),
    enabled: !!view,
  });

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['database', page.id] });
    void qc.invalidateQueries({ queryKey: ['rows', page.id] });
  };
  const setValue = useMutation({
    mutationFn: ({ rowID, propID, value }: { rowID: string; propID: string; value: unknown }) =>
      setRowValues(rowID, { [propID]: value }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['rows', page.id] }),
  });
  const addRow = useMutation({
    mutationFn: () => createPage(page.space_id, { parent_id: page.id, title: '' }),
    onSuccess: () => {
      invalidate();
      void qc.invalidateQueries({ queryKey: ['tree', page.space_id] });
    },
  });
  const addProp = useMutation({
    mutationFn: () =>
      createProperty(page.id, {
        name: propName.trim(),
        type: propType,
        options: propType === 'select' || propType === 'multi_select' ? { choices: [] } : undefined,
      }),
    onSuccess: () => {
      setAddingProp(false);
      setPropName('');
      invalidate();
    },
  });
  const addView = (kind: DBView['kind']) => {
    const name = kind === 'table' ? 'Table' : kind === 'board' ? 'Board' : 'Calendar';
    void createView(page.id, { name, kind }).then((v) => {
      invalidate();
      setViewID(v.id);
    });
  };
  const saveConfig = (config: DBView['config']) => {
    if (!view) return;
    void updateView(view.id, { config }).then(invalidate);
  };

  if (schema.isLoading) return <LoadingRow />;
  if (schema.isError) return <p className="text-sm text-destructive">{(schema.error as Error).message}</p>;
  const properties = schema.data?.properties ?? [];

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center gap-1">
        {views.map((v) => {
          const Icon = VIEW_ICONS[v.kind];
          return (
            <button
              key={v.id}
              type="button"
              className={cn(
                'flex items-center gap-1.5 rounded-pill px-3 py-1 text-sm font-medium',
                view?.id === v.id ? 'bg-accent text-accent-foreground' : 'text-muted-foreground hover:bg-secondary'
              )}
              onClick={() => setViewID(v.id)}
            >
              <Icon className="w-3.5 h-3.5" />
              {v.name}
            </button>
          );
        })}
        {canEdit ? (
          <div className="flex items-center gap-0.5 text-muted-foreground">
            {(['table', 'board', 'calendar'] as const).map((kind) => {
              const Icon = VIEW_ICONS[kind];
              return (
                <button
                  key={kind}
                  type="button"
                  title={`Add a ${kind} view`}
                  aria-label={`Add a ${kind} view`}
                  className="p-1 rounded hover:bg-secondary"
                  onClick={() => addView(kind)}
                >
                  <Icon className="w-3.5 h-3.5" />
                </button>
              );
            })}
            {view && views.length > 1 ? (
              <button
                type="button"
                title="Delete this view"
                aria-label="Delete this view"
                className="p-1 rounded hover:bg-secondary"
                onClick={() => {
                  void deleteView(view.id).then(() => {
                    setViewID(null);
                    invalidate();
                  });
                }}
              >
                <Trash2 className="w-3.5 h-3.5" />
              </button>
            ) : null}
          </div>
        ) : null}
        <div className="flex-1" />
        {canEdit ? (
          <>
            <Button variant="secondary" size="sm" onClick={() => setAddingProp(!addingProp)}>
              <Plus className="w-3.5 h-3.5" /> Column
            </Button>
            <Button size="sm" disabled={addRow.isPending} onClick={() => addRow.mutate()}>
              <Plus className="w-3.5 h-3.5" /> Row
            </Button>
          </>
        ) : null}
      </div>

      {addingProp ? (
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (propName.trim()) addProp.mutate();
          }}
        >
          <Input
            autoFocus
            value={propName}
            placeholder="Column name"
            className="h-8 w-48 text-sm"
            onChange={(e) => setPropName(e.target.value)}
          />
          <Select
            value={propType}
            className="h-8 text-sm"
            aria-label="Column type"
            onChange={(e) => setPropType(e.target.value as PropertyType)}
          >
            {PROPERTY_TYPES.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </Select>
          <Button type="submit" size="sm" disabled={!propName.trim() || addProp.isPending}>
            Add
          </Button>
          {addProp.isError ? <span className="text-xs text-destructive">{(addProp.error as Error).message}</span> : null}
        </form>
      ) : null}

      {rows.isLoading ? <LoadingRow /> : null}
      {setValue.isError ? <p className="text-xs text-destructive">{(setValue.error as Error).message}</p> : null}
      {view && rows.data ? (
        view.kind === 'table' ? (
          <TableView
            dbID={page.id}
            properties={properties}
            rows={rows.data}
            view={view}
            canEdit={canEdit}
            onSetValue={(rowID, propID, value) => setValue.mutate({ rowID, propID, value })}
          />
        ) : view.kind === 'board' ? (
          <BoardView
            properties={properties}
            rows={rows.data}
            view={view}
            canEdit={canEdit}
            onSetValue={(rowID, propID, value) => setValue.mutate({ rowID, propID, value })}
            onConfig={saveConfig}
          />
        ) : (
          <CalendarView properties={properties} rows={rows.data} view={view} onConfig={saveConfig} />
        )
      ) : null}
    </section>
  );
}
