import React from 'react'
import {createRoot} from 'react-dom/client'
import './style.css'
import App from './App'

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
