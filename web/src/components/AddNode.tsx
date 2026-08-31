import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "./ui/sheet";
import { formatTimestamp, t } from "../i18n";

/**
 * Add a node, and enrol it.
 *
 * These are one flow rather than two screens because a node without an
 * enrolment token is inert: the row exists, the host knows nothing about it,
 * and nothing on the node list says what to do next. Both endpoints existed
 * and neither was reachable from the browser, so adding a node meant the CLI.
 *
 * The token is shown ONCE. Only its hash is stored, so there is no second
 * chance to read it -- the panel can only issue a new one, which invalidates
 * whatever the operator was in the middle of pasting. That is why the command
 * is presented as the primary artefact and the raw token beside it.
 */
export function AddNode({ open, onOpenChange }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [enrolled, setEnrolled] = useState<{
    nodeID: number;
    nodeName: string;
    command: string;
    token: string;
    expiresAt: number;
  } | null>(null);

  const create = useMutation({
    mutationFn: async (body: { name: string; address: string }) => {
      const node = await api.post<{ id: number }>("/api/v1/nodes", body);
      // Enrolment follows immediately. A node created without a token is a row
      // an operator has to remember to come back to, and the panel gives them
      // nothing to come back to it FOR.
      const enrol = await api.post<{
        token: string;
        command: string;
        expires_at: number;
      }>(`/api/v1/nodes/${node.id}/enroll-token`, {});
      return { node, enrol, name: body.name };
    },
    onSuccess: ({ node, enrol, name: nodeName }) => {
      setEnrolled({
        nodeID: node.id,
        nodeName,
        command: enrol.command,
        token: enrol.token,
        expiresAt: enrol.expires_at,
      });
      queryClient.invalidateQueries({ queryKey: ["nodes"] });
    },
  });

  function close() {
    setName("");
    setAddress("");
    setEnrolled(null);
    create.reset();
    onOpenChange(false);
  }

  return (
    <Sheet open={open} onOpenChange={(o) => (o ? onOpenChange(true) : close())}>
      <SheetContent aria-describedby={undefined}>
        <SheetHeader>
          <SheetTitle>{enrolled ? t("node.enrolTitle") : t("node.add")}</SheetTitle>
        </SheetHeader>

        {enrolled === null ? (
          <div className="space-y-3">
            <div>
              <label className="block text-xs text-muted-foreground" htmlFor="node-name">
                {t("node.name")}
              </label>
              <Input id="node-name" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div>
              <label className="block text-xs text-muted-foreground" htmlFor="node-address">
                {t("node.address")}
              </label>
              <Input
                id="node-address"
                value={address}
                onChange={(e) => setAddress(e.target.value)}
              />
              {/* The address is what clients connect to and what the agent is
                  reached on. Saying so beats an operator entering a private
                  address that works from the panel and from nowhere else. */}
              <p className="mt-1 text-xs text-muted-foreground">{t("node.addressHint")}</p>
            </div>

            <MutationError error={create.error} />

            <div className="flex gap-2">
              <Button
                size="sm"
                disabled={name.trim() === "" || address.trim() === "" || create.isPending}
                onClick={() => create.mutate({ name: name.trim(), address: address.trim() })}
              >
                {t("node.addAndEnrol")}
              </Button>
              <Button size="sm" variant="outline" onClick={close}>
                {t("cancel")}
              </Button>
            </div>
          </div>
        ) : (
          <EnrolmentInstructions enrolled={enrolled} onDone={close} />
        )}
      </SheetContent>
    </Sheet>
  );
}

function EnrolmentInstructions({
  enrolled,
  onDone,
}: {
  enrolled: {
    nodeID: number;
    nodeName: string;
    command: string;
    token: string;
    expiresAt: number;
  };
  onDone: () => void;
}) {
  return (
    <div className="space-y-3">
      <p className="text-sm">
        {t("node.enrolCreated", { name: enrolled.nodeName })}
      </p>

      {/* Stated before the token is shown, not after: an operator who closes
          this and comes back looking for it needs to have been warned. */}
      <p className="text-xs text-warning" role="status">
        {t("node.enrolOnce")}
      </p>

      <div>
        <p className="text-xs text-muted-foreground">{t("node.enrolCommand")}</p>
        <pre className="overflow-x-auto rounded border border-border bg-card p-2 font-mono text-[11px]">
          {enrolled.command}
        </pre>
        <CopyButton value={enrolled.command} />
      </div>

      <div>
        <p className="text-xs text-muted-foreground">{t("node.enrolToken")}</p>
        <code className="block overflow-x-auto rounded border border-border bg-card px-2 py-1 font-mono text-xs">
          {enrolled.token}
        </code>
      </div>

      <p className="text-xs text-muted-foreground">
        {t("node.enrolExpires", { at: formatTimestamp(enrolled.expiresAt) })}
      </p>

      <Button size="sm" onClick={onDone}>
        {t("done")}
      </Button>
    </div>
  );
}

/** Re-issue for a node that already exists — a token expired, or was lost. */
export function ReissueEnrolment({ nodeId, nodeName }: { nodeId: number; nodeName: string }) {
  const [result, setResult] = useState<{ command: string; expiresAt: number } | null>(null);

  const issue = useMutation({
    mutationFn: () =>
      api.post<{ token: string; command: string; expires_at: number }>(
        `/api/v1/nodes/${nodeId}/enroll-token`,
        {},
      ),
    onSuccess: (r) => setResult({ command: r.command, expiresAt: r.expires_at }),
  });

  return (
    <div>
      <Button size="sm" variant="outline" onClick={() => issue.mutate()} disabled={issue.isPending}>
        {t("node.reissueEnrol")}
      </Button>
      {/* Issuing a new token INVALIDATES the previous one. An operator who is
          mid-paste needs to know that before they click, not after. */}
      <p className="mt-1 text-xs text-muted-foreground">{t("node.reissueWarning")}</p>
      <MutationError error={issue.error} />
      {result && (
        <div className="mt-2">
          <pre className="overflow-x-auto rounded border border-border bg-card p-2 font-mono text-[11px]">
            {result.command}
          </pre>
          <CopyButton value={result.command} />
          <p className="mt-1 text-xs text-muted-foreground">
            {t("node.enrolExpires", { at: formatTimestamp(result.expiresAt) })}
          </p>
        </div>
      )}
      <span className="sr-only">{nodeName}</span>
    </div>
  );
}

function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      size="sm"
      variant="outline"
      className="mt-1"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // A denied clipboard permission is not worth a dialog: the value is
          // on screen and selectable either way.
          setCopied(false);
        }
      }}
    >
      {copied ? t("sub.copied") : t("sub.copy")}
    </Button>
  );
}
