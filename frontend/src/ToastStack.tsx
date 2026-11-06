import {useSyncExternalStore} from 'react';
import {notifications, ToastProgress} from './notifications';
import './toastStack.css';

// ToastStack renders the shared notification store (notifications.ts) inside
// App's bottom-right .floating-cards column. The store keeps newest first and
// the stack lays out column-reverse, so the newest toast sits nearest the
// corner. Hovering the stack pauses every auto-dismiss timer, so a toast
// being read never vanishes under the pointer.
export function ToastStack() {
  const toasts = useSyncExternalStore(notifications.subscribe, notifications.getSnapshot);
  if (toasts.length === 0) {
    return null;
  }
  return (
    <div className="toast-stack" onMouseEnter={notifications.pause} onMouseLeave={notifications.resume}>
      {toasts.map((toast) => (
        <div key={toast.id} className={`toast toast-${toast.kind}`} role={toast.kind === 'error' ? 'alert' : 'status'}>
          <div className="toast-body">
            <strong>
              {toast.title}
              {toast.count > 1 ? <span className="toast-count"> ×{toast.count}</span> : null}
            </strong>
            {toast.message ? <span className="toast-message">{toast.message}</span> : null}
            {toast.detail ? <span className="toast-detail">{toast.detail}</span> : null}
            {toast.progress ? <ToastMeter progress={toast.progress} /> : null}
          </div>
          {toast.action ? (
            <button
              type="button"
              className="toast-action"
              onClick={() => {
                toast.action?.run();
                notifications.dismiss(toast.id);
              }}
            >
              {toast.action.label}
            </button>
          ) : null}
          <button type="button" className="toast-dismiss" aria-label="Dismiss notification" onClick={() => notifications.dismiss(toast.id)}>
            ×
          </button>
        </div>
      ))}
    </div>
  );
}

function ToastMeter({progress}: {progress: ToastProgress}) {
  const fraction = progress.total > 0 ? Math.min(1, progress.done / progress.total) : 0;
  return (
    <span className="toast-meter">
      <span className="toast-meter-track">
        <span className="toast-meter-fill" style={{width: `${Math.round(fraction * 100)}%`}} />
      </span>
      {progress.total > 0 ? <span className="toast-meter-count">{progress.done}/{progress.total}</span> : null}
    </span>
  );
}
