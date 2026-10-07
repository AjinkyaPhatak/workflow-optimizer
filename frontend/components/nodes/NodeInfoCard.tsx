"use client";

import { categoryClass, iconText, originLabel } from "@/features/nodes/catalog";
import type { Issue } from "@/lib/workflow/issues";
import { authNote, sideEffectsNote, usesCredential } from "@/lib/workflow/labels";
import type { NodeDefinition, PortDefinition } from "@/types/api";

function Ports({ title, ports }: { title: string; ports: PortDefinition[] }) {
  if (ports.length === 0) return null;
  return (
    <div className="info-section">
      <div className="info-heading">{title}</div>
      <ul className="info-ports">
        {ports.map((p) => (
          <li key={p.name}>
            <span className="info-port-name">{p.name}</span>
            <span className="info-port-type">{p.type}</span>
            {p.required && <span className="info-port-req">required</span>}
            {p.multiple && <span className="info-port-req">multiple</span>}
            {p.description && <div className="info-port-desc">{p.description}</div>}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** What a node does, from its backend NodeDefinition: description, ports,
 * whether it uses a credential or changes external systems, and (on the
 * canvas) its current validation findings. */
export function NodeInfoCard({ def, title, issues = [] }: { def: NodeDefinition | undefined; title?: string; issues?: Issue[] }) {
  if (!def) {
    return (
      <div className="node-info" data-testid="node-info">
        <div className="info-title">{title ?? "Unknown node"}</div>
        <p className="info-desc">This node type is not in the node catalog.</p>
      </div>
    );
  }
  const credential = usesCredential(def);
  const auth = authNote(def);
  const side = sideEffectsNote(def.side_effects);
  return (
    <div className="node-info" data-testid="node-info">
      <div className="info-header">
        <span className={`node-icon ${categoryClass(def.category)}`}>{iconText(def)}</span>
        <div>
          <div className="info-title">{title ?? def.name}</div>
          <div className="info-sub">
            {title && title !== def.name ? `${def.name} · ` : ""}
            {originLabel(def)}
          </div>
        </div>
      </div>
      {def.description && <p className="info-desc">{def.description}</p>}
      <Ports title="Inputs" ports={def.inputs} />
      <Ports title="Outputs" ports={def.outputs} />
      {(credential || side) && (
        <div className="info-section info-notes">
          {credential && (
            <div><span className="info-tag">Credential</span>{auth ?? `${credential.required ? "Requires" : "Can use"} a workspace credential.`}</div>
          )}
          {side && <div><span className="info-tag warn">Side effects</span>{side}</div>}
        </div>
      )}
      {issues.length > 0 && (
        <div className="info-section info-issues">
          {issues.map((i) => (
            <div key={i.key}>⚠ {i.message}</div>
          ))}
        </div>
      )}
    </div>
  );
}
