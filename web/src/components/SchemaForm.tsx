import { t } from "../i18n";

/**
 * A JSON Schema as the adapters publish it.
 *
 * Only the keywords the adapters actually use are modelled. Anything else in a
 * schema is ignored by the form and still enforced by the panel, which is the
 * safe direction: an unmodelled keyword means a field renders more permissively
 * than it should and the server refuses it, never the reverse.
 */
export interface JSONSchema {
  type?: string;
  properties?: Record<string, JSONSchema>;
  required?: string[];
  additionalProperties?: boolean;
  description?: string;
  enum?: string[];
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
  pattern?: string;
  items?: JSONSchema;
}

export type Params = Record<string, unknown>;

/**
 * Which disclosure group a field belongs to.
 *
 * The spec asks for basic, then transport, then security. Schemas carry no such
 * grouping, so it is derived from the field name -- and that derivation can be
 * wrong, which is what `other` is for. A field the heuristic does not recognise
 * is shown in its own group rather than hidden: a misgrouped field is a small
 * annoyance, a field the operator cannot reach at all is a protocol they cannot
 * configure.
 */
export type Group = "basic" | "transport" | "security" | "other";

const SECURITY = /(tls|cert|key|sni|reality|password|obfs|auth|fingerprint|alpn)/i;
const TRANSPORT = /(transport|network|ws|websocket|grpc|path|host|header|mtu|keepalive|port|listen|subnet|dns|masquerade|bandwidth|mbps)/i;

/**
 * groupOf classifies one field.
 *
 * Required fields are always basic regardless of their name: they are what the
 * operator must supply to create anything, so burying a required field behind a
 * disclosure produces a form that cannot be submitted and does not say why.
 */
export function groupOf(name: string, required: boolean): Group {
  if (required) return "basic";
  if (SECURITY.test(name)) return "security";
  if (TRANSPORT.test(name)) return "transport";
  return "other";
}

/** The groups present in a schema, in disclosure order. */
export function groupsOf(schema: JSONSchema): Group[] {
  const required = new Set(schema.required ?? []);
  const seen = new Set<Group>();
  for (const name of Object.keys(schema.properties ?? {})) {
    seen.add(groupOf(name, required.has(name)));
  }
  return (["basic", "transport", "security", "other"] as const).filter((g) => seen.has(g));
}

function labelFor(group: Group): string {
  switch (group) {
    case "basic":
      return t("studio.groupBasic");
    case "transport":
      return t("studio.groupTransport");
    case "security":
      return t("studio.groupSecurity");
    default:
      return t("studio.groupOther");
  }
}

/**
 * SchemaForm renders one adapter's params from the schema it publishes.
 *
 * The panel holds no protocol knowledge and neither does this: there is no list
 * of protocols here, no special case for Xray or WireGuard. A protocol the
 * adapters gain tomorrow renders with no change, and one they do not support
 * cannot be typed at all, because the fields come from the schema.
 */
export function SchemaForm({
  schema,
  value,
  onChange,
}: {
  schema: JSONSchema;
  value: Params;
  onChange: (next: Params) => void;
}) {
  const required = new Set(schema.required ?? []);
  const properties = Object.entries(schema.properties ?? {});

  if (properties.length === 0) {
    return <p className="text-xs text-muted-foreground">{t("studio.noFields")}</p>;
  }

  return (
    <div className="space-y-4">
      {groupsOf(schema).map((group) => {
        const inGroup = properties.filter(
          ([name]) => groupOf(name, required.has(name)) === group,
        );
        return (
          <fieldset key={group} className="space-y-3">
            <legend className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {labelFor(group)}
            </legend>
            {inGroup.map(([name, field]) => (
              <SchemaField
                key={name}
                name={name}
                field={field}
                required={required.has(name)}
                value={value[name]}
                onChange={(v) => {
                  const next = { ...value };
                  // Clearing a field REMOVES it rather than storing "". An
                  // optional string left blank would otherwise be submitted as
                  // an empty value the schema may well refuse, and the operator
                  // never typed anything.
                  if (v === undefined || v === "") {
                    delete next[name];
                  } else {
                    next[name] = v;
                  }
                  onChange(next);
                }}
              />
            ))}
          </fieldset>
        );
      })}
    </div>
  );
}

function SchemaField({
  name,
  field,
  required,
  value,
  onChange,
}: {
  name: string;
  field: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (next: unknown) => void;
}) {
  const id = "field-" + name;
  const label = (
    <label className="block text-xs text-muted-foreground" htmlFor={id}>
      {name}
      {required && <span className="ms-1 text-destructive">{t("studio.requiredMark")}</span>}
    </label>
  );
  const hint = field.description ? (
    <p className="mt-1 text-xs text-muted-foreground">{field.description}</p>
  ) : null;

  // An enum is a closed set, so it is a select and nothing else can be typed.
  // This is where "unsupported options are absent, not disabled" is enforced
  // for free: an option the adapter did not publish is not in the list.
  if (field.enum && field.enum.length > 0) {
    return (
      <div>
        {label}
        <select
          id={id}
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.target.value)}
          className="w-full rounded border border-input bg-background px-2 py-1 text-sm"
        >
          <option value="">{t("studio.unset")}</option>
          {field.enum.map((option) => (
            <option key={option} value={option}>
              {option === "" ? t("studio.emptyOption") : option}
            </option>
          ))}
        </select>
        {hint}
      </div>
    );
  }

  if (field.type === "boolean") {
    return (
      <div>
        <label className="flex items-center gap-2 text-xs text-foreground" htmlFor={id}>
          <input
            id={id}
            type="checkbox"
            checked={value === true}
            onChange={(e) => onChange(e.target.checked ? true : undefined)}
          />
          {name}
        </label>
        {hint}
      </div>
    );
  }

  if (field.type === "integer" || field.type === "number") {
    return (
      <div>
        {label}
        <input
          id={id}
          type="number"
          min={field.minimum}
          max={field.maximum}
          value={typeof value === "number" ? String(value) : ""}
          onChange={(e) => {
            const raw = e.target.value;
            if (raw === "") {
              onChange(undefined);
              return;
            }
            const parsed = Number(raw);
            // A number field must submit a NUMBER. Sending the string would be
            // refused by the schema, and the operator would be looking at a
            // field that appears correct.
            onChange(Number.isNaN(parsed) ? undefined : parsed);
          }}
          className="w-full rounded border border-input bg-background px-2 py-1 text-sm"
        />
        {hint}
      </div>
    );
  }

  if (field.type === "array") {
    // Comma-separated, because every array an adapter publishes today is an
    // array of scalars (dns servers, alpn). A schema with object items would
    // need a real editor, and rendering one badly is worse than sending the
    // operator to JSON mode, which is always available.
    const asText = Array.isArray(value) ? value.join(", ") : "";
    return (
      <div>
        {label}
        <input
          id={id}
          type="text"
          value={asText}
          onChange={(e) => {
            const parts = e.target.value
              .split(",")
              .map((s) => s.trim())
              .filter((s) => s !== "");
            onChange(parts.length === 0 ? undefined : parts);
          }}
          className="w-full rounded border border-input bg-background px-2 py-1 text-sm"
        />
        <p className="mt-1 text-xs text-muted-foreground">{t("studio.commaSeparated")}</p>
        {hint}
      </div>
    );
  }

  return (
    <div>
      {label}
      <input
        id={id}
        type="text"
        minLength={field.minLength}
        maxLength={field.maxLength}
        value={typeof value === "string" ? value : ""}
        onChange={(e) => onChange(e.target.value)}
        className="w-full rounded border border-input bg-background px-2 py-1 text-sm"
      />
      {hint}
    </div>
  );
}
