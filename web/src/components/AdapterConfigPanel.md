# AdapterConfigPanel Component

## Overview

`AdapterConfigPanel` is a comprehensive protocol adapter configuration UI for the antimage control plane. It provides dynamic form generation from JSON Schema, protocol-specific help, and real-time validation for all 5 protocol adapters (Xray, WireGuard, Hysteria2, L2TP/IPsec, sing-box).

## Features

1. **Adapter Capability Discovery**: Fetches available adapters from `/api/v1/nodes/{id}/adapters`
2. **JSON Schema-driven Forms**: Renders configuration forms from `adapter.Caps.ServiceSchema`
3. **Protocol-specific Help**: Displays examples and requirements for each adapter
4. **Dual Edit Modes**: Switch between form-based and raw JSON editing
5. **Real-time Validation**: Server-side validation with field-level error attribution
6. **Hot-add Warnings**: Displays adapter capabilities (PKI requirements, restart behavior)
7. **Service Management**: Create, edit, and delete service configurations
8. **Dark Theme**: Consistent with antimage's design system

## Usage

```tsx
import { AdapterConfigPanel } from "../components/AdapterConfigPanel";

function NodeDetailsPage({ nodeId }: { nodeId: number }) {
  return (
    <div>
      <h1>Node Configuration</h1>
      <AdapterConfigPanel nodeId={nodeId} />
    </div>
  );
}
```

## Protocol Support

### Xray (xray-core)
- **Protocols**: VLESS, VMess, Trojan
- **Transports**: TCP, WebSocket, gRPC
- **Security**: TLS/none
- **Hot user add**: Supported (via gRPC API)

### WireGuard
- **Required**: port, subnet (CIDR), private_key
- **Optional**: dns, mtu, keepalive
- **Hot user add**: Supported (via `wg set`)

### Hysteria2
- **Required**: port, password, cert_file, key_file
- **Optional**: up_mbps, down_mbps, obfs, masquerade
- **Hot user add**: Not supported (requires restart)
- **PKI**: Required

### L2TP/IPsec
- **Required**: ip_range, local_ip, psk
- **Optional**: dns_servers
- **Hot user add**: Supported (credential reload)

### sing-box
- **Protocols**: VLESS, VMess, Trojan, Shadowsocks
- **Transports**: TCP, WebSocket
- **Security**: TLS/none
- **Hot user add**: Not supported (requires restart)

## API Endpoints

### GET /api/v1/nodes/{id}/adapters
Returns adapters the node reported at Hello:
```json
{
  "adapters": [
    {
      "kind": "xray",
      "version": "1",
      "capabilities": ["hot_user_add", "routing"],
      "service_schema": { /* JSON Schema */ },
      "requires_pki": false,
      "hot_user_add": true
    }
  ]
}
```

### GET /api/v1/nodes/{id}/services
Returns configured services:
```json
{
  "services": [
    {
      "id": 1,
      "node_id": 42,
      "adapter_kind": "xray",
      "params": {
        "protocol": "vless",
        "port": 443,
        "network": "ws",
        "security": "tls"
      },
      "enabled": true,
      "created_at": 1735234567
    }
  ]
}
```

### POST /api/v1/nodes/{id}/services
Create a new service:
```json
{
  "adapter_kind": "xray",
  "params": {
    "protocol": "vless",
    "port": 443,
    "network": "tcp",
    "security": "tls",
    "cert_file": "/path/to/cert.pem",
    "key_file": "/path/to/key.pem"
  }
}
```

### PUT /api/v1/services/{id}
Update service configuration:
```json
{
  "params": {
    "protocol": "vless",
    "port": 8443,
    "network": "ws",
    "path": "/ws"
  }
}
```

### DELETE /api/v1/services/{id}
Remove a service (disconnects all users).

## Design Decisions

### Adapter-derived UI
The UI never hardcodes protocol knowledge. If `adapter.Caps` doesn't declare a capability, the UI doesn't offer it. This prevents operators from configuring features the node cannot execute.

### JSON Schema validation
Validation happens server-side against the node's copy of the schema. The frontend provides UX feedback but doesn't duplicate validation logic—preventing drift between panel and UI.

### Collapsible sections
Services are displayed in collapsible cards to handle nodes with many configurations without overwhelming the viewport.

### Protocol help text
Inline examples reduce documentation lookups and show common patterns for each adapter.

### Form/JSON toggle
Advanced users can edit raw JSON while preserving the structured form for common cases. Both modes submit through the same validation path.

## Internationalization

All UI strings are localized across 5 languages (en, fa, ru, zh-CN, ar) with strict key parity enforced by CI.

## Integration Points

- **InboundStudio**: Existing component for service management (predecessor)
- **EgressPanel**: Outbound configuration (uses same adapter capabilities)
- **NodeAdapters**: Displays adapter metadata (capabilities, versions)
- **SchemaForm**: Reusable JSON Schema form renderer

## Future Enhancements

1. **Field-level validation feedback**: Real-time validation as user types
2. **Configuration templates**: Save/load common configurations
3. **Bulk operations**: Apply same config across multiple nodes
4. **Diff view**: Compare current vs. desired configuration
5. **Migration wizard**: Convert configs between adapters
