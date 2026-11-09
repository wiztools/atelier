import {videoThumbnailOnlySrc} from '../mediaIntake';
import {AudioPlayer} from '../media/AudioPlayer';
import {LazyPanelVideo} from '../media/LazyPanelVideo';
import {assetTurnLabel} from '../composerMentions';
import {transcriptChipLabel} from '../historyContent';
import {AppModel} from '../app/useAppModel';

export function AssetsPanel({app}: {app: AppModel}) {
  return (
    <>
          <aside className="assets-panel" aria-label={app.composerLibraryID ? 'Library assets' : 'Conversation assets'}>
            <div className="assets-panel-header">
              <span className="assets-panel-title">
                {app.composerLibraryID && app.composerProjectNames ? `Assets · ${app.composerProjectNames.libraryName}` : 'Assets'}
              </span>
              {/* The ＋ actions open session-free drafts — a draft on a
                  conversation-less new chat opens DETACHED and materializes
                  its host conversation at the first save, so the icons need
                  no conversation to be useful (the original gap: they were
                  hidden until the first send created one). */}
              <button
                type="button"
                className="assets-panel-new-composition"
                disabled={app.editorOpening}
                aria-label="New comic"
                title="Open the comic editor on an unsaved draft — add art from the picker inside it"
                onClick={() => void app.openNewComic(app.activeConversationID, [])}
              >
                {/* A comic page: a framed grid with a plus. */}
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <rect x="3" y="4" width="18" height="16" rx="1.5" strokeWidth="2" />
                  <path d="M3 12h18M12 4v8" strokeWidth="2" />
                  <path d="M19 15.5v5" strokeWidth="2" />
                  <path d="M16.5 18h5" strokeWidth="2" />
                </svg>
              </button>
              <button
                type="button"
                className="assets-panel-new-composition"
                disabled={app.editorOpening}
                aria-label="New composition"
                title="Open the video composer on an unsaved draft — add clips from the picker inside it"
                onClick={() => void app.openNewComposition(app.activeConversationID, [])}
              >
                {/* Staggered clips on tracks + a plus — deliberately
                    media-neutral: the composer hosts audio-only mixes too. */}
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="M3 5.5h9" strokeWidth="3" />
                  <path d="M7 12h7" strokeWidth="3" />
                  <path d="M3 18.5h7" strokeWidth="3" />
                  <path d="M18.5 15v6" strokeWidth="2" />
                  <path d="M15.5 18h6" strokeWidth="2" />
                </svg>
              </button>
              <button
                type="button"
                className="assets-panel-new-composition"
                disabled={app.editorOpening}
                aria-label="New audio mix"
                title="Open the composer on an audio-only draft — arrange music, voice, and sound on the audio lanes; a video added later turns it into a video sequence"
                onClick={() => void app.openNewComposition(app.activeConversationID, [], true)}
              >
                {/* A note head + stem with an equalizer bar and a plus — the
                    audio-first sibling of the composition glyph. */}
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <circle cx="7.5" cy="16.5" r="2.5" strokeWidth="2" />
                  <path d="M10 16.5V4.5l6 1.5" strokeWidth="2" />
                  <path d="M15 12v2M18 10v4" strokeWidth="2" />
                  <path d="M19.5 17v4M17.5 19h4" strokeWidth="2" />
                </svg>
              </button>
              <button
                type="button"
                className="assets-panel-close"
                onClick={() => app.setAssetsPanelOpen(false)}
                aria-label="Close assets panel"
                title="Close assets panel"
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
                  <path d="M18 6 6 18M6 6l12 12" />
                </svg>
              </button>
            </div>
            <div className="assets-list">
              {app.panelAssets.length === 0 ? (
                <p className="assets-empty">
                  {app.composerLibraryID
                    ? 'No library assets yet. Uploads and generated media from every chat in this library show up here.'
                    : 'No assets yet. Images, audio, video, and subtitles — attached, generated, or converted — show up here as the conversation produces them.'}
                </p>
              ) : (
                app.panelAssets.map((asset) => (
                  <figure key={`${asset.id}-${asset.originTurnId}`} className={`asset-card asset-${asset.kind}`}>
                    <div className="asset-preview">
                      {asset.kind === 'image' && asset.url ? (
                        <button
                          type="button"
                          className="asset-image-button"
                          onClick={() => {
                            app.setPreviewImage(asset.url || '');
                            app.setPreviewEditContext(app.editArtifactIDFromURL(asset.url || '') && asset.conversationId
                              ? {conversationID: asset.conversationId, artifactID: app.editArtifactIDFromURL(asset.url || '') ?? asset.id}
                              : null);
                          }}
                          aria-label="Preview image asset"
                          title="Preview image asset"
                        >
                          <img src={asset.url} alt="" loading="lazy" />
                        </button>
                      ) : asset.kind === 'audio' && asset.url ? (
                        <AudioPlayer src={asset.url} />
                      ) : asset.kind === 'video' && asset.url ? (
                        <LazyPanelVideo src={asset.url} />
                      ) : asset.kind === 'transcript' && asset.url ? (
                        <span className="asset-transcript-chip">
                          {transcriptChipLabel(asset.url)}
                        </span>
                      ) : asset.kind === 'markdown' && asset.url ? (
                        <span className="asset-transcript-chip">{asset.url.toLowerCase().endsWith('.fountain') ? '📝 Screenplay (.fountain)' : '📝 Markdown (.md)'}</span>
                      ) : asset.kind === 'composition' && asset.url ? (
                        <span className="asset-transcript-chip">🎬 Composition project</span>
                      ) : asset.kind === 'comic' && asset.url ? (
                        <span className="asset-transcript-chip">📚 Comic book</span>
                      ) : (
                        <span className="asset-missing">Artifact file is missing on disk</span>
                      )}
                      {/* Sibling of the media (never nested inside the image
                          zoom button — a button cannot contain a button) so the
                          hover reveal overlays every preview variant without
                          stealing the zoom/play gestures. */}
                      {/* Compositions and comics are not @-mentionable (v1):
                          a mention has no slot and the model cannot read the
                          project, so the affordance would be a dead end. */}
                      {asset.kind !== 'composition' && asset.kind !== 'comic' ? (
                        <button
                          type="button"
                          className="asset-reference-button"
                          // Suppress the mousedown default so the click can't
                          // steal focus from the composer: engines that focus
                          // buttons on mousedown (Chromium) would blur the
                          // textarea before onClick fires, defeating the
                          // caret-position insert in insertAssetReference.
                          // Keyboard focus (Tab) is unaffected.
                          onMouseDown={(event) => event.preventDefault()}
                          onClick={() => app.insertAssetReference(asset)}
                          aria-label="Reference this asset in the composer"
                          title="Reference this asset in the composer"
                        >
                          <span aria-hidden="true">@</span>
                        </button>
                      ) : null}
                      {/* Same sibling rule as the reference button; hidden when
                          the artifact is missing on disk — there is nothing to
                          save. */}
                      {asset.url ? (
                        <button
                          type="button"
                          className="asset-download-button"
                          onClick={() => app.downloadAsset(asset)}
                          aria-label="Download asset"
                          title="Download asset"
                        >
                          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M12 4v11" />
                            <path d="m7 10 5 5 5-5" />
                            <path d="M5 20h14" />
                          </svg>
                        </button>
                      ) : null}
                      {asset.kind === 'image' && asset.url && asset.conversationId ? (
                        <button
                          type="button"
                          className="asset-edit-button"
                          disabled={app.editorOpening}
                          onClick={() => void app.openImageEditor(asset.conversationId, asset.id)}
                          aria-label="Edit image with AI"
                          title="Edit image with AI"
                        >
                          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M12 20h9" />
                            <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                          </svg>
                        </button>
                      ) : null}
                      {/* The video editor's trim stage plays the clip, so a container
                          WebKit cannot decode gets no Edit door here either. */}
                      {asset.kind === 'video' && asset.url && asset.conversationId && !videoThumbnailOnlySrc(asset.url) ? (
                        <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                          onClick={() => void app.openVideoEditor(asset.conversationId, asset.id)}
                          aria-label="Edit video" title="Edit video">
                          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M12 20h9" />
                            <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                          </svg>
                        </button>
                      ) : null}
                      {asset.kind === 'audio' && asset.url && asset.conversationId ? (
                        <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                          onClick={() => void app.openAudioEditor(asset.conversationId, asset.id)}
                          aria-label="Edit audio" title="Edit audio">
                          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M12 20h9" />
                            <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                          </svg>
                        </button>
                      ) : null}
                      {asset.kind === 'transcript' && asset.url && asset.conversationId ? (
                        <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                          onClick={() => void app.openSubtitleEditor(asset.conversationId, asset.id)}
                          aria-label="Edit subtitles" title="Edit subtitles">
                          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M12 20h9" />
                            <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                          </svg>
                        </button>
                      ) : null}
                      {asset.kind === 'markdown' && asset.url && asset.conversationId ? (
                        <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                          onClick={() => void app.openMarkdownEditor(asset.conversationId, asset.id)}
                          aria-label="Edit document" title="Edit document">
                          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M12 20h9" />
                            <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                          </svg>
                        </button>
                      ) : null}
                      {asset.kind === 'comic' && asset.url && asset.conversationId ? (
                        <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                          onClick={() => void app.openComicEditor(asset.conversationId, asset.id)}
                          aria-label="Open comic" title="Open this comic revision in the comic editor">
                          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M12 20h9" />
                            <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                          </svg>
                        </button>
                      ) : null}
                      {asset.kind === 'composition' && asset.url && asset.conversationId ? (
                        <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                          onClick={() => void app.openCompositionEditor(asset.conversationId, asset.id)}
                          aria-label="Open composition" title="Open this composition revision in the video composer">
                          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M12 20h9" />
                            <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                          </svg>
                        </button>
                      ) : null}
                      {/* Navigation needs no artifact on disk — unlike download,
                          a missing file still has a conversation to jump to. */}
                      {asset.conversationId ? (
                        <button
                          type="button"
                          className="asset-conversation-button"
                          onClick={() => app.openAssetConversation(asset)}
                          aria-label={
                            app.composerLibraryID && asset.conversationId !== app.activeConversationID
                              ? `Open ${asset.conversationTitle || 'conversation'}`
                              : 'Jump to this asset in the conversation'
                          }
                          title={
                            app.composerLibraryID && asset.conversationId !== app.activeConversationID
                              ? `Open in ${asset.conversationTitle || 'conversation'}`
                              : 'Jump to the turn that produced this asset'
                          }
                        >
                          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
                          </svg>
                        </button>
                      ) : null}
                    </div>
                    <figcaption>
                      <span className="asset-kind">{asset.kind}</span>
                      <span className="asset-meta">
                        {asset.role === 'user' ? 'attached' : 'generated'} · {app.composerLibraryID ? (asset.conversationTitle || 'chat') : assetTurnLabel(asset.originTurnId)}
                      </span>
                    </figcaption>
                  </figure>
                ))
              )}
            </div>
          </aside>
    </>
  );
}
