import * as React from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { PauseCircle, CheckCircle2 } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { formatRelativeDate } from "@/lib/format";
import type { WebhookEndpoint } from "@/lib/api/webhooks";

interface WebhooksTableProps {
  webhooks: WebhookEndpoint[];
  loading?: boolean;
}

export function WebhooksTable({
  webhooks,
  loading,
}: WebhooksTableProps): React.ReactElement {
  return (
    <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[45%]">Endpoint</TableHead>
            <TableHead>Events</TableHead>
            <TableHead>Status</TableHead>
            <TableHead className="hidden md:table-cell">Created</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {loading
            ? Array.from({ length: 4 }).map((_, i) => <SkeletonRow key={i} />)
            : webhooks.map((w) => <Row key={w.endpoint_id} webhook={w} />)}
        </TableBody>
      </Table>
    </div>
  );
}

function Row({ webhook }: { webhook: WebhookEndpoint }): React.ReactElement {
  const navigate = useNavigate();
  return (
    <TableRow
      interactive
      onClick={() =>
        void navigate({
          to: "/webhooks/$id",
          params: { id: webhook.endpoint_id },
        })
      }
    >
      <TableCell>
        <Link
          to="/webhooks/$id"
          params={{ id: webhook.endpoint_id }}
          onClick={(e) => e.stopPropagation()}
          className="block min-w-0"
        >
          <div className="truncate font-mono text-xs text-[var(--color-fg)]">
            {webhook.url}
          </div>
          <div className="mt-0.5 font-mono text-[10px] text-[var(--color-fg-subtle)]">
            {webhook.endpoint_id.slice(0, 12)}
          </div>
        </Link>
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-1">
          {webhook.events.slice(0, 2).map((e) => (
            <Badge key={e} tone="accent" className="font-mono">
              {e}
            </Badge>
          ))}
          {webhook.events.length > 2 ? (
            <Badge tone="neutral">+{webhook.events.length - 2}</Badge>
          ) : null}
        </div>
      </TableCell>
      <TableCell>
        {webhook.active ? (
          <Badge tone="success">
            <CheckCircle2 className="size-3" /> Active
          </Badge>
        ) : (
          <Badge tone="neutral">
            <PauseCircle className="size-3" /> Paused
          </Badge>
        )}
      </TableCell>
      <TableCell className="hidden text-xs text-[var(--color-fg-muted)] md:table-cell">
        {formatRelativeDate(webhook.created_at)}
      </TableCell>
    </TableRow>
  );
}

function SkeletonRow(): React.ReactElement {
  return (
    <TableRow>
      <TableCell>
        <div className="space-y-1.5">
          <Skeleton className="h-3 w-64" />
          <Skeleton className="h-2.5 w-24" />
        </div>
      </TableCell>
      <TableCell>
        <div className="flex gap-1">
          <Skeleton className="h-5 w-24 rounded-full" />
          <Skeleton className="h-5 w-12 rounded-full" />
        </div>
      </TableCell>
      <TableCell>
        <Skeleton className="h-5 w-16 rounded-full" />
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <Skeleton className="h-3 w-24" />
      </TableCell>
    </TableRow>
  );
}
