import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { Trash2 } from 'lucide-react';
import { createSpace, deleteSpace, updateSpace, type GrantLevel, type Space } from '@/api/wiki';
import { Button } from '@/components/ui/Button';
import { Dialog, Select } from '@/components/ui/Dialog';
import { Input, Label } from '@/components/ui/Input';

/** Creates a team space, or edits one when `space` is given. */
export function SpaceDialog({ space, onClose }: { space?: Space; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState(space?.name ?? '');
  const [icon, setIcon] = useState(space?.icon ?? '📚');
  const [description, setDescription] = useState(space?.description ?? '');
  const [everyone, setEveryone] = useState<GrantLevel | ''>('');
  const isPrivate = space?.kind === 'private';

  const save = useMutation({
    mutationFn: () => {
      const body = { name: name.trim(), icon: icon.trim(), description: description.trim() };
      if (space) return updateSpace(space.id, isPrivate ? { icon: body.icon, description: body.description } : body);
      return createSpace({ ...body, everyone_level: everyone });
    },
    onSuccess: (saved) => {
      void qc.invalidateQueries({ queryKey: ['spaces'] });
      void qc.invalidateQueries({ queryKey: ['space', saved.id] });
      onClose();
      if (!space) void navigate({ to: '/s/$spaceId', params: { spaceId: saved.id } });
    },
  });
  const remove = useMutation({
    mutationFn: () => deleteSpace(space!.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['spaces'] });
      onClose();
      void navigate({ to: '/' });
    },
  });
  const error = save.error ?? remove.error;

  return (
    <Dialog title={space ? 'Space settings' : 'New team space'} onClose={onClose}>
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) save.mutate();
        }}
      >
        <div className="flex gap-2">
          <div className="w-16">
            <Label htmlFor="space-icon">Icon</Label>
            <Input id="space-icon" value={icon} maxLength={8} onChange={(e) => setIcon(e.target.value)} />
          </div>
          <div className="flex-1">
            <Label htmlFor="space-name">Name</Label>
            <Input
              id="space-name"
              value={name}
              disabled={isPrivate}
              onChange={(e) => setName(e.target.value)}
              autoFocus={!space}
            />
          </div>
        </div>
        <div>
          <Label htmlFor="space-description">Description</Label>
          <Input id="space-description" value={description} onChange={(e) => setDescription(e.target.value)} />
        </div>
        {!space ? (
          <div>
            <Label htmlFor="space-everyone">Who can see it</Label>
            <Select
              id="space-everyone"
              className="w-full"
              value={everyone}
              onChange={(e) => setEveryone(e.target.value as GrantLevel | '')}
            >
              <option value="">Only people I invite</option>
              <option value="view">Everyone signed in can view</option>
              <option value="edit">Everyone signed in can edit</option>
            </Select>
          </div>
        ) : null}
        <div className="flex items-center gap-2 pt-1">
          {space && !isPrivate ? (
            <Button
              type="button"
              variant="ghost"
              className="text-destructive"
              disabled={remove.isPending}
              onClick={() => {
                if (window.confirm(`Delete ${space.name} and every page in it? This cannot be undone.`)) remove.mutate();
              }}
            >
              <Trash2 className="w-4 h-4" />
              Delete space
            </Button>
          ) : null}
          <div className="flex-1" />
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={!name.trim() || save.isPending}>
            {space ? 'Save' : 'Create space'}
          </Button>
        </div>
        {error ? <p className="text-sm text-destructive">{(error as Error).message}</p> : null}
      </form>
    </Dialog>
  );
}
