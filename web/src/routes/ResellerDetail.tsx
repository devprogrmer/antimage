import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { Limit, MutationError } from "./Resellers";
import type { Reseller } from "./Resellers";
import { formatNumber, formatRelativeTime, formatTimestamp, t } from "../i18n";

interface Movement {
  id: number;
  delta: number;
  reason: string;
  subject_id: number | null;
  note: string;
  at: number;
}

/** A key unique to one attempt, so a retry after a timeout cannot mint or
 *  charge twice. Generated per form instance rather than per submit: a retry of
 *  the SAME attempt must reuse it, which is the entire point. */
function newIdempotencyKey(): string {
  return `ui-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

export function ResellerDetail({ resellerID }: { resellerID: number }) {
  const session = useSession();

  const reseller = useQuery({
    queryKey: ["reseller", resellerID],
    queryFn: () => api.get<Reseller>(`/api/v1/resellers/${resellerID}`),
  });

  const balance = useQuery({
    queryKey: ["reseller", resellerID, "balance"],
    queryFn: () =>
      api.get<{ reseller_id: number; balance: number }>(
        `/api/v1/resellers/${resellerID}/balance`,
      ),
  });

  if (reseller.isError) return <MutationError error={reseller.error} />;
  if (!reseller.data) return <p className="text-sm text-zinc-500">{t("common.loading")}</p>;

  const record = reseller.data;
  const funded = balance.data !== undefined && balance.data.balance > record.credit_floor;

  return (
    <div className="space-y-4">
      <div className="flex items-baseline gap-3">
        <h2 className="font-mono text-lg font-semibold">{record.display_name}</h2>
        {record.enabled ? (
          <span className="text-xs text-green-500">{t("reseller.enabled")}</span>
        ) : (
          <span className="text-xs text-zinc-500">{t("reseller.disabled")}</span>
        )}
      </div>

      <section className="rounded border border-zinc-800 bg-zinc-900 p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("reseller.account")}</h3>
        <dl className="grid grid-cols-2 gap-2 text-sm">
          <dt className="text-xs text-zinc-400">{t("reseller.balance")}</dt>
          <dd className={funded ? "font-mono text-green-400" : "font-mono text-amber-400"}>
            {balance.data === undefined ? "—" : formatNumber(balance.data.balance)}
          </dd>
          <dt className="text-xs text-zinc-400">{t("reseller.creditFloor")}</dt>
          <dd className="font-mono">{formatNumber(record.credit_floor)}</dd>
          <dt className="text-xs text-zinc-400">{t("reseller.maxSubjects")}</dt>
          <dd>
            <Limit value={record.max_subjects} />
          </dd>
          <dt className="text-xs text-zinc-400">{t("reseller.maxQuota")}</dt>
          <dd>
            <Limit value={record.max_quota_bytes} />
          </dd>
          <dt className="text-xs text-zinc-400">{t("reseller.adminId")}</dt>
          <dd className="font-mono">{formatNumber(record.admin_id)}</dd>
          <dt className="text-xs text-zinc-400">{t("reseller.updated")}</dt>
          <dd className="font-mono text-xs text-zinc-500">
            {formatTimestamp(record.updated_at)}
          </dd>
        </dl>
        {!funded && balance.data !== undefined && (
          <p className="mt-3 text-xs text-amber-400" role="status">
            {t("reseller.atFloorWarning")}
          </p>
        )}
      </section>

      {can(session.data, "reseller:write") && <SettingsForm record={record} />}
      {can(session.data, "credit:grant") && <GrantCreditForm resellerID={resellerID} />}
      {can(session.data, "subject:write") && <ProvisionForm resellerID={resellerID} />}

      <Ledger resellerID={resellerID} />

      {can(session.data, "reseller:write") && <DangerZone record={record} />}
    </div>
  );
}

/**
 * Editing the record, including both ceilings.
 *
 * Every field is sent on every save, and an unlimited ceiling is sent as an
 * explicit null. The API distinguishes absent (leave alone) from null (set to
 * unlimited) precisely so unlimited stays reachable once a limit has been set;
 * a form that omitted a cleared field could never clear one.
 */
function SettingsForm({ record }: { record: Reseller }) {
  const queryClient = useQueryClient();
  const [displayName, setDisplayName] = useState(record.display_name);
  const [enabled, setEnabled] = useState(record.enabled);
  const [creditFloor, setCreditFloor] = useState(String(record.credit_floor));
  const [subjectsUnlimited, setSubjectsUnlimited] = useState(record.max_subjects === null);
  const [maxSubjects, setMaxSubjects] = useState(String(record.max_subjects ?? 0));
  const [quotaUnlimited, setQuotaUnlimited] = useState(record.max_quota_bytes === null);
  const [maxQuota, setMaxQuota] = useState(String(record.max_quota_bytes ?? 0));

  const save = useMutation({
    mutationFn: () =>
      api.put(`/api/v1/resellers/${record.id}`, {
        display_name: displayName,
        enabled,
        credit_floor: Number(creditFloor),
        max_subjects: subjectsUnlimited ? null : Number(maxSubjects),
        max_quota_bytes: quotaUnlimited ? null : Number(maxQuota),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["reseller", record.id] });
      queryClient.invalidateQueries({ queryKey: ["resellers"] });
    },
  });

  return (
    <section className="rounded border border-zinc-800 bg-zinc-900 p-4">
      <h3 className="mb-3 text-sm font-semibold">{t("reseller.settings")}</h3>
      <div className="space-y-3">
        <div>
          <label className="block text-xs text-zinc-400" htmlFor="edit-display-name">
            {t("reseller.displayName")}
          </label>
          <input
            id="edit-display-name"
            type="text"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm"
          />
        </div>

        <label className="flex items-center gap-2 text-xs text-zinc-300">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          {t("reseller.enabledLabel")}
        </label>

        <div>
          <label className="block text-xs text-zinc-400" htmlFor="edit-credit-floor">
            {t("reseller.creditFloor")}
          </label>
          <input
            id="edit-credit-floor"
            type="number"
            value={creditFloor}
            onChange={(e) => setCreditFloor(e.target.value)}
            className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm"
          />
          <p className="mt-1 text-xs text-zinc-500">{t("reseller.creditFloorHint")}</p>
        </div>

        <LimitField
          id="edit-max-subjects"
          label={t("reseller.maxSubjects")}
          unlimited={subjectsUnlimited}
          onUnlimited={setSubjectsUnlimited}
          value={maxSubjects}
          onValue={setMaxSubjects}
        />
        <LimitField
          id="edit-max-quota"
          label={t("reseller.maxQuota")}
          unlimited={quotaUnlimited}
          onUnlimited={setQuotaUnlimited}
          value={maxQuota}
          onValue={setMaxQuota}
        />

        <MutationError error={save.error} />
        <button
          type="button"
          onClick={() => save.mutate()}
          disabled={!displayName || save.isPending}
          className="rounded bg-blue-600 px-3 py-1 text-sm hover:bg-blue-700 disabled:opacity-50"
        >
          {t("save")}
        </button>
        {save.isSuccess && !save.isPending && (
          <span className="ms-2 text-xs text-green-500" role="status">
            {t("common.saved")}
          </span>
        )}
      </div>
    </section>
  );
}

/**
 * A ceiling, where "unlimited" and "zero" are separate answers.
 *
 * The number input stays mounted while unlimited is checked so the previous
 * figure is still there when it is unchecked; zero remains typeable and means
 * "may create nothing", which is a real setting and not an empty one.
 */
function LimitField({
  id,
  label,
  unlimited,
  onUnlimited,
  value,
  onValue,
}: {
  id: string;
  label: string;
  unlimited: boolean;
  onUnlimited: (next: boolean) => void;
  value: string;
  onValue: (next: string) => void;
}) {
  return (
    <div>
      <label className="block text-xs text-zinc-400" htmlFor={id}>
        {label}
      </label>
      <div className="flex items-center gap-2">
        <input
          id={id}
          type="number"
          min="0"
          value={value}
          disabled={unlimited}
          onChange={(e) => onValue(e.target.value)}
          className="w-40 rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm disabled:opacity-40"
        />
        <label className="flex items-center gap-1 text-xs text-zinc-300">
          <input
            type="checkbox"
            checked={unlimited}
            onChange={(e) => onUnlimited(e.target.checked)}
          />
          {t("reseller.unlimited")}
        </label>
      </div>
      <p className="mt-1 text-xs text-zinc-500">{t("reseller.zeroIsNotUnlimited")}</p>
    </div>
  );
}

/**
 * Funding a tenant.
 *
 * Separate from the settings form because it takes a separate permission:
 * credit:grant is not implied by reseller:write, since minting credit is the
 * only operation that creates value from nothing. Anyone who could rename a
 * tenant would otherwise be able to pay themselves.
 */
function GrantCreditForm({ resellerID }: { resellerID: number }) {
  const queryClient = useQueryClient();
  const [delta, setDelta] = useState("");
  const [note, setNote] = useState("");
  const [key, setKey] = useState(newIdempotencyKey);

  const grant = useMutation({
    mutationFn: () =>
      api.post<{ ledger_id: number; balance: number }>(
        `/api/v1/resellers/${resellerID}/credit`,
        { delta: Number(delta), reason: "topup", note, idempotency_key: key },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["reseller", resellerID] });
      setDelta("");
      setNote("");
      // A new key only AFTER one succeeds. Rotating it on failure would turn a
      // retry into a second grant, which is the case the key exists to prevent.
      setKey(newIdempotencyKey());
    },
  });

  return (
    <section className="rounded border border-zinc-800 bg-zinc-900 p-4">
      <h3 className="mb-3 text-sm font-semibold">{t("reseller.grantCredit")}</h3>
      <div className="space-y-3">
        <div>
          <label className="block text-xs text-zinc-400" htmlFor="grant-delta">
            {t("reseller.amount")}
          </label>
          <input
            id="grant-delta"
            type="number"
            value={delta}
            onChange={(e) => setDelta(e.target.value)}
            className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm"
          />
          <p className="mt-1 text-xs text-zinc-500">{t("reseller.amountHint")}</p>
        </div>
        <div>
          <label className="block text-xs text-zinc-400" htmlFor="grant-note">
            {t("reseller.note")}
          </label>
          <input
            id="grant-note"
            type="text"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm"
          />
        </div>
        <MutationError error={grant.error} />
        {grant.isSuccess && (
          <p className="text-xs text-green-500" role="status">
            {t("reseller.granted")}
          </p>
        )}
        <button
          type="button"
          onClick={() => grant.mutate()}
          disabled={!delta || Number(delta) === 0 || grant.isPending}
          className="rounded bg-blue-600 px-3 py-1 text-sm hover:bg-blue-700 disabled:opacity-50"
        >
          {t("reseller.grant")}
        </button>
      </div>
    </section>
  );
}

/**
 * Creating a customer owned by this tenant, debiting them for it.
 *
 * This is the operation the whole engine exists for, and until the API landed
 * it was reachable only through CSV import. The debit and the creation share
 * one transaction server-side, so a customer nobody paid for and a charge for a
 * customer who does not exist are both impossible.
 *
 * No service picker: there is no flat service listing endpoint -- services
 * belong to nodes -- so this matches the existing subject create form and
 * provisions with none attached.
 */
function ProvisionForm({ resellerID }: { resellerID: number }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [cost, setCost] = useState("0");
  const [quota, setQuota] = useState("0");
  const [key, setKey] = useState(newIdempotencyKey);

  const provision = useMutation({
    mutationFn: () =>
      api.post<{ subject_id: number; balance: number }>(
        `/api/v1/resellers/${resellerID}/subjects`,
        {
          name,
          note: "",
          service_ids: [],
          cost: Number(cost),
          quota_bytes: Number(quota),
          idempotency_key: key,
        },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["reseller", resellerID] });
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
      setName("");
      setKey(newIdempotencyKey());
    },
  });

  return (
    <section className="rounded border border-zinc-800 bg-zinc-900 p-4">
      <h3 className="mb-3 text-sm font-semibold">{t("reseller.provision")}</h3>
      <div className="space-y-3">
        <div>
          <label className="block text-xs text-zinc-400" htmlFor="provision-name">
            {t("subject.name")}
          </label>
          <input
            id="provision-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs text-zinc-400" htmlFor="provision-cost">
            {t("reseller.cost")}
          </label>
          <input
            id="provision-cost"
            type="number"
            min="0"
            value={cost}
            onChange={(e) => setCost(e.target.value)}
            className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm"
          />
          <p className="mt-1 text-xs text-zinc-500">{t("reseller.costHint")}</p>
        </div>
        <div>
          <label className="block text-xs text-zinc-400" htmlFor="provision-quota">
            {t("reseller.quotaBytes")}
          </label>
          <input
            id="provision-quota"
            type="number"
            min="0"
            value={quota}
            onChange={(e) => setQuota(e.target.value)}
            className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm"
          />
          <p className="mt-1 text-xs text-zinc-500">{t("reseller.quotaHint")}</p>
        </div>
        <MutationError error={provision.error} />
        {provision.isSuccess && (
          <p className="text-xs text-green-500" role="status">
            {t("reseller.provisioned")}
          </p>
        )}
        <button
          type="button"
          onClick={() => provision.mutate()}
          disabled={!name || provision.isPending}
          className="rounded bg-blue-600 px-3 py-1 text-sm hover:bg-blue-700 disabled:opacity-50"
        >
          {t("reseller.provisionAction")}
        </button>
      </div>
    </section>
  );
}

/**
 * The ledger is append-only and the balance is its sum, never a stored figure.
 * Showing the movements rather than a single number is what makes a balance
 * auditable: every change has a row, a reason and a time.
 */
function Ledger({ resellerID }: { resellerID: number }) {
  const ledger = useQuery({
    queryKey: ["reseller", resellerID, "ledger"],
    queryFn: () =>
      api.get<{ movements: Movement[] }>(`/api/v1/resellers/${resellerID}/ledger`),
  });

  return (
    <section className="rounded border border-zinc-800 bg-zinc-900 p-4">
      <h3 className="mb-3 text-sm font-semibold">{t("reseller.ledger")}</h3>
      {ledger.isError && <MutationError error={ledger.error} />}
      {ledger.data?.movements.length === 0 && (
        <p className="text-sm text-zinc-500">{t("reseller.ledgerEmpty")}</p>
      )}
      {ledger.data !== undefined && ledger.data.movements.length > 0 && (
        <table className="w-full border-collapse text-sm text-zinc-200">
          <thead>
            <tr className="border-b border-zinc-800 text-xs uppercase tracking-wide text-zinc-500">
              <th className="py-2 pe-3 text-start">{t("reseller.amount")}</th>
              <th className="pe-3 text-start">{t("reseller.reason")}</th>
              <th className="pe-3 text-start">{t("reseller.note")}</th>
              <th className="text-start">{t("reseller.when")}</th>
            </tr>
          </thead>
          <tbody>
            {ledger.data.movements.map((movement) => (
              <tr key={movement.id} className="border-b border-zinc-900">
                <td
                  className={
                    movement.delta < 0
                      ? "py-1.5 pe-3 font-mono text-amber-400"
                      : "py-1.5 pe-3 font-mono text-green-400"
                  }
                >
                  {formatNumber(movement.delta)}
                </td>
                <td className="pe-3 font-mono text-xs">{movement.reason}</td>
                <td className="pe-3 text-xs text-zinc-400">{movement.note}</td>
                <td className="text-xs text-zinc-500">{formatRelativeTime(movement.at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

/**
 * Deletion is refused while the tenant owns customers, and the refusal says how
 * many. Cascading would remove a tenant's live customers along with the tenant,
 * so the foreign key restricts instead -- and deactivating is offered here
 * because it is what an operator usually means: it stops provisioning without
 * cutting anybody off, and it is reversible.
 */
function DangerZone({ record }: { record: Reseller }) {
  const queryClient = useQueryClient();

  const remove = useMutation({
    mutationFn: () => api.del(`/api/v1/resellers/${record.id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["resellers"] });
    },
  });

  const deactivate = useMutation({
    mutationFn: () => api.put(`/api/v1/resellers/${record.id}`, { enabled: false }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["reseller", record.id] });
      queryClient.invalidateQueries({ queryKey: ["resellers"] });
    },
  });

  return (
    <section className="rounded border border-red-900 bg-zinc-900 p-4">
      <h3 className="mb-1 text-sm font-semibold text-red-400">{t("reseller.dangerZone")}</h3>
      <p className="mb-3 text-xs text-zinc-400">{t("reseller.deleteExplain")}</p>
      <div className="flex gap-2">
        {record.enabled && (
          <button
            type="button"
            onClick={() => deactivate.mutate()}
            disabled={deactivate.isPending}
            className="rounded bg-zinc-800 px-3 py-1 text-sm hover:bg-zinc-700 disabled:opacity-50"
          >
            {t("reseller.deactivate")}
          </button>
        )}
        <button
          type="button"
          onClick={() => {
            if (confirm(t("reseller.confirmDelete"))) remove.mutate();
          }}
          disabled={remove.isPending}
          className="rounded bg-red-700 px-3 py-1 text-sm hover:bg-red-600 disabled:opacity-50"
        >
          {t("delete")}
        </button>
      </div>
      <MutationError error={remove.error} />
      <MutationError error={deactivate.error} />
    </section>
  );
}
