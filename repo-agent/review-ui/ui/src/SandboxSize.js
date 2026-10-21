import React from 'react';

// sandboxSizeFields are the gear's sandbox size inputs: the spec field,
// its label, and factory's default (an empty field keeps it).
const sandboxSizeFields = [
  { key: 'diskSize', label: 'Workspace disk', placeholder: '10Gi', title: 'The workspace PVC: the checkout, caches and anything kept under /workspaces.' },
  { key: 'storageClass', label: 'Disk class', placeholder: 'cluster default', title: 'The workspace PVC\'s StorageClass, e.g. premium-rwo.' },
  { key: 'ephemeralStorage', label: 'Ephemeral storage', placeholder: '6Gi', title: 'The pod\'s local disk: /tmp and anything written outside /workspaces. A pod over it is evicted — building a large repository needs more than the default.' },
  { key: 'cpuRequest', label: 'CPU request', placeholder: '500m' },
  { key: 'cpuLimit', label: 'CPU limit', placeholder: '4' },
  { key: 'memoryRequest', label: 'Memory request', placeholder: '2Gi' },
  { key: 'memoryLimit', label: 'Memory limit', placeholder: '6Gi' },
];

// SandboxSize edits the size the board's sandboxes are made with. Each
// field is a Kubernetes quantity; empty keeps factory's default.
function SandboxSize({ spec, onChange }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: 'small' }}>
      <div style={{ fontWeight: 600 }}>
        Sandbox size <span style={{ fontWeight: 400, color: 'var(--text-secondary)' }}>(applies to sandboxes made after saving, research sessions included; empty = factory default)</span>
      </div>
      <div style={{ display: 'flex', gap: '12px', flexWrap: 'wrap' }}>
        {sandboxSizeFields.map(f => (
          <label key={f.key} style={{ display: 'flex', flexDirection: 'column', gap: '2px' }} title={f.title || ''}>
            {f.label}
            <input type="text" value={spec[f.key] || ''} placeholder={f.placeholder} style={{ width: '110px' }}
              onChange={e => onChange({ ...spec, [f.key]: e.target.value })} />
          </label>
        ))}
      </div>
    </div>
  );
}

export default SandboxSize;
