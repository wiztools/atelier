import React from 'react'
import {createRoot} from 'react-dom/client'
import './style.css'
import App from './App'

// Packaged builds must not surface WebKit's default context menu (Reload/Inspect
// Element); `wails dev` keeps it as the way into the debugger. Editable fields
// keep the native Cut/Copy/Paste menu.
function isEditableTarget(target: EventTarget | null): boolean {
    if (!(target instanceof Element)) return false
    const field = target.closest('input, textarea')
    if (field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement) {
        return !field.disabled && !field.readOnly
    }
    const holder = target.closest('[contenteditable]')
    return holder instanceof HTMLElement && holder.isContentEditable
}

if (import.meta.env.PROD) {
    window.addEventListener('contextmenu', (event) => {
        if (!isEditableTarget(event.target)) event.preventDefault()
    })
}

const container = document.getElementById('root')

type ErrorBoundaryState = {
    error: Error | null
}

class ErrorBoundary extends React.Component<React.PropsWithChildren, ErrorBoundaryState> {
    state: ErrorBoundaryState = {error: null}

    static getDerivedStateFromError(error: Error): ErrorBoundaryState {
        return {error}
    }

    render() {
        if (this.state.error) {
            return (
                <main className="boot-error">
                    <h1>Atelier could not start.</h1>
                    <p>{this.state.error.message}</p>
                </main>
            )
        }
        return this.props.children
    }
}

if (!container) {
    throw new Error('Missing #root container')
}

const root = createRoot(container)

root.render(
    <React.StrictMode>
        <ErrorBoundary>
            <App/>
        </ErrorBoundary>
    </React.StrictMode>
)
