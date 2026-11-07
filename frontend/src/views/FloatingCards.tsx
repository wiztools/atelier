import {ToastStack} from '../ToastStack';
import {shortenHomePath} from '../utils';
import {hasToolPermissionDetails, toolPermissionSummary, toolPermissionTitle} from '../toolPermission';
import {AppModel} from '../app/useAppModel';

export function FloatingCards({app}: {app: AppModel}) {
  return (
    <>
          <div className="floating-cards">
            {app.editorOpening && app.view === 'app' ? (
              <div className="editor-preparation-status" role="status">
                <span>{app.videoEditorOpening ? 'Preparing video for editing…' : app.audioEditorOpening ? 'Preparing audio for editing…' : app.documentCompareOpening ? 'Preparing comparison…' : 'Preparing image for editing…'}</span>
                {app.videoEditorOpening ? (
                  <button type="button" onClick={() => {
                    app.cancelVideoPreparation();
                  }}>Cancel</button>
                ) : null}
              </div>
            ) : null}
            {app.startupError ? (
              <div className="startup-error">
                <strong>Atelier started with a local data warning.</strong>
                <span>{app.startupError}</span>
                <button type="button" onClick={() => app.setStartupError('')}>Dismiss</button>
              </div>
            ) : null}
            {app.updateAvailable ? (
              <div className="update-banner">
                <div className="update-banner-content">
                  <strong>Atelier {app.updateAvailable.latest} is available.</strong>
                  {app.updateAvailable.notes ? <span>{app.updateAvailable.notes}</span> : null}
                  {app.updateQueued ? (
                    <span className="update-queued">Installing when the current conversation finishes…</span>
                  ) : null}
                  {app.updateError ? <span className="update-error">{app.updateError}</span> : null}
                </div>
                <div className="update-banner-actions">
                  <button onClick={app.dismissUpdate} disabled={app.updateBusy}>Later</button>
                  <button className="primary" onClick={app.requestUpdateInstall} disabled={app.updateBusy}>
                    {app.updateBusy ? 'Installing…' : 'Install & Relaunch'}
                  </button>
                </div>
              </div>
            ) : null}
            <ToastStack />
            {app.toolPermissions.length ? (
              <div className="tool-permission-panel">
                {app.toolPermissions.map((permission) => (
                  <div className="tool-permission-card" key={permission.id}>
                    <div className="tool-permission-content">
                      <strong>{toolPermissionTitle(permission)}</strong>
                      <span>{toolPermissionSummary(permission)}</span>
                      {permission.cwd ? <small>in {shortenHomePath(permission.cwd)}</small> : null}
                      {hasToolPermissionDetails(permission) ? (
                        <details className="tool-permission-details">
                          <summary>Details</summary>
                          {permission.command?.length ? (
                            <div>
                              <small>Command</small>
                              <code>{permission.command.join(' ')}</code>
                            </div>
                          ) : null}
                          {permission.summary && permission.summary !== toolPermissionSummary(permission) ? (
                            <div>
                              <small>Summary</small>
                              <pre>{permission.summary}</pre>
                            </div>
                          ) : null}
                          {permission.path ? (
                            <div>
                              <small>Path</small>
                              <code>{shortenHomePath(permission.path)}</code>
                            </div>
                          ) : null}
                          {permission.contentPreview ? (
                            <div>
                              <small>Preview</small>
                              <pre>{permission.contentPreview}</pre>
                            </div>
                          ) : null}
                        </details>
                      ) : null}
                    </div>
                    <div className="tool-permission-actions">
                      <button onClick={() => app.resolveToolPermission(permission.id, false)}>Deny</button>
                      <button className="primary" onClick={() => app.resolveToolPermission(permission.id, true)}>Allow</button>
                    </div>
                  </div>
                ))}
              </div>
            ) : null}
          </div>
    </>
  );
}
