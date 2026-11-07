import './App.css';
import {useAppModel} from './app/useAppModel';
import {AppOverlays} from './views/AppOverlays';
import {AssetsPanel} from './views/AssetsPanel';
import {ChatView} from './views/ChatView';
import {UsageView} from './views/UsageView';
import {ConversationModelsView} from './views/ConversationModelsView';
import {SettingsView} from './views/SettingsView';
import {EditorSlot} from './views/EditorSlot';
import {FloatingCards} from './views/FloatingCards';
import {Sidebar} from './views/Sidebar';
function App() {
  const app = useAppModel();

  return (
    <main
      ref={app.shellRef}
      className={[
        'shell',
        app.view !== 'app' || app.editorOpen ? 'settings-open' : '',
        app.resizingSidebar ? 'resizing resizing-sidebar' : '',
        app.resizingAssets ? 'resizing resizing-assets' : '',
        app.view === 'app' && app.assetsPanelOpen ? 'assets-open' : '',
      ].filter(Boolean).join(' ')}
      style={app.view !== 'app' || app.editorOpen ? undefined : {
        '--sidebar-width': `${app.sidebarWidth}px`,
        '--assets-width': `${app.assetsWidth}px`,
      } as Record<string, string>}
    >
      {app.view !== 'app' || app.editorOpen ? null : (
        <Sidebar app={app}/>
      )}
      {app.view !== 'app' || app.editorOpen ? null : (
        <div
          className="sidebar-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize sidebar"
          onMouseDown={(event) => {
            event.preventDefault();
            app.setResizingSidebar(true);
          }}
        />
      )}

      <section className="workspace">
        <FloatingCards app={app}/>
        <EditorSlot app={app}/>
        {app.view === 'settings' ? (
        <SettingsView app={app}/>
        ) : app.view === 'conversation-models' ? (
        <ConversationModelsView app={app}/>
        ) : app.view === 'usage' ? (
        <UsageView app={app}/>
        ) : !app.editorOpen ? (
        <ChatView app={app}/>
        ) : null}
      </section>
      {app.view !== 'app' || app.editorOpen || !app.assetsPanelOpen ? null : (
        <div
          className="assets-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize assets panel"
          onMouseDown={(event) => {
            event.preventDefault();
            app.setResizingAssets(true);
          }}
        />
      )}
      {app.view !== 'app' || app.editorOpen || !app.assetsPanelOpen ? null : (
        <AssetsPanel app={app}/>
      )}
      <AppOverlays app={app}/>
    </main>
  );
}

export default App;