// Shared utilities for file-transfer features (companion panels, standalone
// SFTP/SCP connections, transfer menus). Protocol-related helpers that are
// used across components/stores live here.

// Effective file-transfer protocol of a connection. Only SSH connections carry
// the choice; anything else (and legacy configs without the field) is SFTP.
export function fileTransferProto(config?: { type?: string; fileTransferProto?: 'sftp' | 'scp' } | null): 'sftp' | 'scp' {
  return config?.type === 'ssh' && config?.fileTransferProto === 'scp' ? 'scp' : 'sftp'
}

// i18n key for the "connect SFTP / connect SCP" context-menu item of an SSH
// connection, matching its configured file-transfer protocol.
export function connectFileMenuKey(config?: { type?: string; fileTransferProto?: 'sftp' | 'scp' } | null): string {
  return fileTransferProto(config) === 'scp' ? 'sidebar.connectScp' : 'sidebar.connectSftp'
}
