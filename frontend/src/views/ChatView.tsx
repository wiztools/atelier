import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import {DiscardMediaUpload} from '../../wailsjs/go/main/App';
import {composerAcceptAttribute, videoThumbnailOnlySrc} from '../mediaIntake';
import {ChatProviderID} from '../types';
import {asArray, shortenHomePath} from '../utils';
import {ModelCombobox} from '../settings/ModelCombobox';
import {InlineMediaActions, AudioCardActions} from '../media/MediaActions';
import {VideoPlayer} from '../media/VideoPlayer';
import {AudioPlayer} from '../media/AudioPlayer';
import {ModelCapabilityLink} from '../settings/ModelSelectionPanel';
import {TurnUsage, ConversationUsage} from '../usage/TurnUsage';
import {HarnessRunPanel} from '../usage/HarnessRunPanel';
import {StreamingActivity} from '../usage/StreamingActivity';
import {isNearScrollBottom} from '../dom';
import {transcriptChipLabel} from '../historyContent';
import {AppModel} from '../app/useAppModel';

export function ChatView({app}: {app: AppModel}) {
  return (
    <>
            <>
              <div className="toolbar">
                <div className="toolbar-left">
                  {app.composerProjectNames ? (
                    <div className="project-context" title="This chat is scoped to a library project">
                      {app.composerProjectNames.libraryName}{app.composerProjectNames.projectName ? ` › ${app.composerProjectNames.projectName}` : ''}
                    </div>
                  ) : null}
                </div>
                <div className="toolbar-right">
                  {/* Always visible: a one-click toggle for airplane mode (the
                      Settings → Providers checkbox remains the explanatory home).
                      Amber while the mode is on, neutral gray when off — the
                      tooltip names the current state and the action. The flip
                      persists through the debounced settings save. */}
                  <button
                    type="button"
                    className={`assets-toggle airplane-indicator${app.airplaneMode ? ' on' : ''}`}
                    onClick={() => app.setAirplaneMode((current) => !current)}
                    aria-pressed={app.airplaneMode}
                    aria-label={app.airplaneMode ? 'Airplane mode is on — click to turn it off' : 'Airplane mode is off — click to turn it on'}
                    title={app.airplaneMode
                      ? 'Airplane mode is on — Atelier contacts no cloud provider. Click to turn it off.'
                      : 'Airplane mode is off — cloud providers are available. Click to turn it on.'}
                  >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                      <path d="M17.8 19.2 16 11l3.5-3.5C21 6 21.5 4 21 3c-1-.5-3 0-4.5 1.5L13 8 4.8 6.2c-.5-.1-.9.1-1.1.5l-.3.5c-.2.5-.1 1 .3 1.3L9 12l-2 3H4l-1 1 3 2 2 3 1-1v-3l3-2 3.5 5.3c.3.4.8.5 1.3.3l.5-.2c.4-.3.6-.7.5-1.2z" />
                    </svg>
                  </button>
                  {/* The persisted usage report for the active conversation —
                      the aggregate over every turn on disk, not just the ones
                      loaded in this session. */}
                  {app.activeConversationID ? (
                    <button
                      type="button"
                      className="assets-toggle usage-report-toggle"
                      onClick={() => app.openConversationUsageReport(app.activeConversationID)}
                      aria-label="Usage report for this conversation"
                      title="Usage report — cost and tool calls across this conversation"
                    >
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                        <path d="M3 3v18h18" />
                        <path d="M7 15v-4" />
                        <path d="M12 15V7" />
                        <path d="M17 15v-6" />
                      </svg>
                    </button>
                  ) : null}
                  <ConversationUsage usage={app.modelUsage} media={app.mediaUsage} />
                  <button
                    type="button"
                    className={`assets-toggle conversation-models-toggle${app.uiOverridesActive ? ' has-overrides' : ''}`}
                    onClick={app.openConversationModels}
                    aria-label={app.activeConversationID ? 'Model overrides for this conversation' : 'Model overrides for your next conversation'}
                    title={
                      app.activeConversationID
                        ? app.uiOverridesActive
                          ? 'This conversation overrides some model selections — click to change'
                          : 'Override model selections for this conversation'
                        : app.uiOverridesActive
                          ? 'Your next conversation will override some model selections — click to change'
                          : 'Override model selections for the conversation you start next'
                    }
                  >
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M12 2v4" />
                        <path d="m15.9 7.5 3.5-2" />
                        <path d="M15.9 16.5l3.5 2" />
                        <path d="M12 18v4" />
                        <path d="m8.1 16.5-3.5 2" />
                        <path d="m8.1 7.5-3.5-2" />
                        <circle cx="12" cy="12" r="3" />
                      </svg>
                    </button>
                    <button
                      type="button"
                      className={`assets-toggle${app.assetsPanelOpen ? ' active' : ''}`}
                      onClick={() => app.setAssetsPanelOpen((open) => !open)}
                      aria-label={app.assetsPanelOpen ? 'Hide assets panel' : 'Show assets panel'}
                      aria-pressed={app.assetsPanelOpen}
                      title={app.assetsPanelOpen ? 'Hide assets' : 'Show assets'}
                  >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                      <path d="m22 11-1.296-1.296a2.4 2.4 0 0 0-3.408 0L11 16" />
                      <path d="M4 8a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2" />
                      <circle cx="13" cy="7" r="1" fill="currentColor" />
                      <rect x="8" y="2" width="14" height="14" rx="2" />
                    </svg>
                  </button>
                </div>
              </div>

              <div className="chat-panel">
                <div
                  className="transcript"
                  ref={app.transcriptRef}
                  onScroll={(event) => {
                    app.shouldFollowTranscriptRef.current = isNearScrollBottom(event.currentTarget);
                  }}
                >
                  {app.visibleHarnessRun ? <HarnessRunPanel run={app.visibleHarnessRun} /> : null}
                  {asArray(app.chat).length === 0 ? (
                    <div className="empty-state">
                      <h2>{app.emptyPrompt?.heading ?? 'What are we making today?'}</h2>
                      <p>{app.emptyPrompt?.sub ?? 'Describe an image, video, or sound to begin.'}</p>
                    </div>
                  ) : asArray(app.chat).map((entry) => {
                    const thinkingCollapsed = Boolean(entry.thinking && (app.collapsedThinkingIDs[entry.id] ?? !entry.streaming));
                    return (
                      <article key={entry.id} id={`msg-${entry.id}`} className={`message ${entry.role}`}>
                        <div className="message-meta">
                          {entry.role}{entry.streaming ? ' streaming' : ''}
                          {entry.provider ? <span className="turn-provider-badge">{entry.provider}</span> : null}
                        </div>
                        {entry.images?.length ? (
                          entry.role === 'assistant' ? (
                            <div className="chat-image-results">
                              {entry.images.map((image, index) => (
                                <figure key={`${entry.id}-image-${index}`} className="chat-image-card">
                                  <div className="chat-media-stage">
                                    <button
                                      className="chat-image-preview"
                                      type="button"
                                      aria-label={`Open generated image ${index + 1}`}
                                      onClick={() => {
                                        app.setPreviewImage(image);
                                        app.setPreviewEditContext(app.editArtifactIDFromURL(image) && app.activeConversationID
                                          ? {conversationID: app.activeConversationID, artifactID: app.editArtifactIDFromURL(image)!}
                                          : null);
                                      }}
                                    >
                                      <img src={image} alt="Generated result" />
                                    </button>
                                    <InlineMediaActions
                                      downloadTitle="Download image"
                                      onDownload={() => app.saveGeneratedImage(image, index)}
                                      editTitle="Edit image with AI"
                                      editDisabled={app.editorOpening}
                                      onEdit={app.activeConversationID && app.editArtifactIDFromURL(image)
                                        ? () => void app.openImageEditor(app.activeConversationID, app.editArtifactIDFromURL(image)!)
                                        : null}
                                    />
                                  </div>
                                </figure>
                              ))}
                            </div>
                          ) : (
                            <div className="thumb-row">
                              {entry.images.map((image, index) => (
                                <button
                                  key={`${entry.id}-image-${index}`}
                                  className="thumb-button"
                                  type="button"
                                  aria-label={`Open attached image ${index + 1}`}
                                  onClick={() => {
                                    app.setPreviewImage(image);
                                    app.setPreviewEditContext(app.editArtifactIDFromURL(image) && app.activeConversationID
                                      ? {conversationID: app.activeConversationID, artifactID: app.editArtifactIDFromURL(image)!}
                                      : null);
                                  }}
                                >
                                  <img src={image} alt="" />
                                </button>
                              ))}
                            </div>
                          )
                        ) : null}
                        {entry.role === 'user' && entry.audios?.length ? (
                          <div className="chat-user-audios">
                            {entry.audios.map((audio, index) => (
                              <div key={`${entry.id}-audio-${index}`}>
                                <AudioPlayer src={audio} />
                                {/* The Save* bindings read artifact paths, not data:
                                    URLs, so a just-attached clip (the live transcript's
                                    inline form) hides download until the persisted
                                    artifact URL replaces it. */}
                                <AudioCardActions
                                  onDownload={audio.startsWith('/atelier-artifact/') ? () => app.saveGeneratedAudio(audio, index) : null}
                                  editDisabled={app.editorOpening}
                                  onEdit={app.activeConversationID && app.audioArtifactIDFromURL(audio)
                                    ? () => void app.openAudioEditor(app.activeConversationID, app.audioArtifactIDFromURL(audio)!)
                                    : null}
                                />
                              </div>
                            ))}
                          </div>
                        ) : null}
                        {entry.role === 'user' && entry.videos?.length ? (
                          <div className="chat-user-videos">
                            {entry.videos.map((video, index) => (
                              <figure key={`${entry.id}-video-${index}`} className="chat-video-card">
                                <div className="chat-media-stage">
                                  <VideoPlayer src={video} />
                                  {/* The video editor's trim stage plays the clip, so a
                                      container WebKit cannot decode gets download only
                                      (VideoPlayer already renders it as a still). */}
                                  <InlineMediaActions
                                    downloadTitle="Download video"
                                    onDownload={app.videoArtifactIDFromURL(video) ? () => app.saveGeneratedVideo(video, index) : null}
                                    editTitle="Edit video"
                                    editDisabled={app.editorOpening}
                                    onEdit={app.activeConversationID && app.videoArtifactIDFromURL(video) && !videoThumbnailOnlySrc(video)
                                      ? () => void app.openVideoEditor(app.activeConversationID, app.videoArtifactIDFromURL(video)!)
                                      : null}
                                  />
                                </div>
                              </figure>
                            ))}
                          </div>
                        ) : null}
                        {entry.role === 'user' && entry.subtitles?.length ? (
                          <div className="chat-transcript-results">
                            {entry.subtitles.map((subtitle, index) => (
                              <div key={`${entry.id}-subtitle-${index}`} className="chat-transcript-card">
                                <span className="chat-transcript-name">{transcriptChipLabel(subtitle)}</span>
                                <div className="chat-transcript-actions">
                                  <button
                                    type="button"
                                    disabled={app.editorOpening || !app.activeConversationID || !app.transcriptArtifactIDFromURL(subtitle)}
                                    title={app.transcriptArtifactIDFromURL(subtitle) ? 'Open in the subtitle editor' : 'Persisted subtitle required'}
                                    onClick={() => {
                                      const artifactID = app.transcriptArtifactIDFromURL(subtitle);
                                      if (artifactID && app.activeConversationID) {
                                        void app.openSubtitleEditor(app.activeConversationID, artifactID);
                                      }
                                    }}
                                  >
                                    Edit
                                  </button>
                                  {/* The Save binding reads artifact paths, not data:
                                      URLs — a just-attached file (the live entry's inline
                                      form) hides download until the persisted artifact
                                      URL replaces it, the same rule the user-audio
                                      block applies. */}
                                  <button
                                    type="button"
                                    onClick={() => app.saveGeneratedTranscript(subtitle, index)}
                                    disabled={!subtitle.startsWith('/atelier-artifact/')}
                                  >
                                    Download subtitle
                                  </button>
                                </div>
                              </div>
                            ))}
                          </div>
                        ) : null}
                        {entry.role === 'assistant' && entry.videos?.length ? (
                          <div className="chat-video-results">
                            {entry.videos.map((video, index) => (
                              <figure key={`${entry.id}-video-${index}`} className="chat-video-card">
                                <div className="chat-media-stage">
                                  <VideoPlayer src={video} />
                                  <InlineMediaActions
                                    downloadTitle="Download video"
                                    onDownload={() => app.saveGeneratedVideo(video, index)}
                                    editTitle="Edit video"
                                    editDisabled={app.editorOpening}
                                    onEdit={app.activeConversationID && app.videoArtifactIDFromURL(video) && !videoThumbnailOnlySrc(video)
                                      ? () => void app.openVideoEditor(app.activeConversationID, app.videoArtifactIDFromURL(video)!)
                                      : null}
                                  />
                                </div>
                              </figure>
                            ))}
                          </div>
                        ) : null}
                        {entry.role === 'assistant' && entry.audios?.length ? (
                          <div className="chat-audio-results">
                            {entry.audios.map((audio, index) => (
                              <figure key={`${entry.id}-audio-${index}`} className="chat-audio-card">
                                <AudioPlayer src={audio} />
                                <AudioCardActions
                                  onDownload={() => app.saveGeneratedAudio(audio, index)}
                                  editDisabled={app.editorOpening}
                                  onEdit={app.activeConversationID && app.audioArtifactIDFromURL(audio)
                                    ? () => void app.openAudioEditor(app.activeConversationID, app.audioArtifactIDFromURL(audio)!)
                                    : null}
                                />
                              </figure>
                            ))}
                          </div>
                        ) : null}
                        {entry.role === 'assistant' && entry.transcripts?.length ? (
                          <div className="chat-transcript-results">
                            {entry.transcripts.map((transcript, index) => (
                              <div key={`${entry.id}-transcript-${index}`} className="chat-transcript-card">
                                <span className="chat-transcript-name">{transcriptChipLabel(transcript)}</span>
                                <div className="chat-transcript-actions">
                                  {/* Only .vtt/.srt open in the subtitle editor; a
                                      plain .txt transcript has nothing to edit. */}
                                  {app.transcriptArtifactIDFromURL(transcript) && app.activeConversationID ? (
                                    <button
                                      type="button"
                                      disabled={app.editorOpening}
                                      onClick={() => void app.openSubtitleEditor(app.activeConversationID, app.transcriptArtifactIDFromURL(transcript)!)}
                                    >
                                      Edit
                                    </button>
                                  ) : null}
                                  <button type="button" onClick={() => app.saveGeneratedTranscript(transcript, index)}>{transcript.toLowerCase().endsWith('.srt') ? 'Download subtitles' : 'Download transcript'}</button>
                                </div>
                              </div>
                            ))}
                          </div>
                        ) : null}
                        {entry.markdowns?.length ? (
                          <div className="chat-transcript-results">
                            {entry.markdowns.map((markdown, index) => (
                              <div key={`${entry.id}-markdown-${index}`} className="chat-transcript-card">
                                <span className="chat-transcript-name">📝 {markdown.split('/').pop() || 'Document (.md)'}</span>
                                <div className="chat-transcript-actions">
                                  <button
                                    type="button"
                                    disabled={app.editorOpening || !app.activeConversationID || !app.markdownArtifactIDFromURL(markdown)}
                                    title={app.markdownArtifactIDFromURL(markdown) ? (markdown.toLowerCase().endsWith('.fountain') ? 'Open in the screenplay editor' : 'Open in the markdown editor') : 'Persisted document required'}
                                    onClick={() => {
                                      const artifactID = app.markdownArtifactIDFromURL(markdown);
                                      if (artifactID && app.activeConversationID) {
                                        void app.openMarkdownEditor(app.activeConversationID, artifactID);
                                      }
                                    }}
                                  >
                                    Edit
                                  </button>
                                  <button
                                    type="button"
                                    onClick={() => app.saveGeneratedMarkdown(markdown, index)}
                                    disabled={!markdown.startsWith('/atelier-artifact/')}
                                  >
                                    Download document
                                  </button>
                                </div>
                              </div>
                            ))}
                          </div>
                        ) : null}
                        {entry.compositions?.length ? (
                          <div className="chat-transcript-results">
                            {entry.compositions.map((composition, index) => (
                              <div key={`${entry.id}-composition-${index}`} className="chat-transcript-card">
                                <span className="chat-transcript-name">🎬 Composition · {(composition.split('/').pop() || 'project.json').replace(/\.json$/i, '').slice(0, 16)}</span>
                                <div className="chat-transcript-actions">
                                  <button
                                    type="button"
                                    disabled={app.editorOpening || !app.activeConversationID || !app.compositionArtifactIDFromURL(composition)}
                                    title={app.compositionArtifactIDFromURL(composition) ? 'Open this revision in the video composer' : 'Persisted composition required'}
                                    onClick={() => {
                                      const artifactID = app.compositionArtifactIDFromURL(composition);
                                      if (artifactID && app.activeConversationID) {
                                        void app.openCompositionEditor(app.activeConversationID, artifactID);
                                      }
                                    }}
                                  >
                                    Edit
                                  </button>
                                  <button
                                    type="button"
                                    onClick={() => app.saveGeneratedComposition(composition, index)}
                                    disabled={!composition.startsWith('/atelier-artifact/')}
                                  >
                                    Download project
                                  </button>
                                </div>
                              </div>
                            ))}
                          </div>
                        ) : null}
                        {entry.comics?.length ? (
                          <div className="chat-transcript-results">
                            {entry.comics.map((comic, index) => (
                              <div key={`${entry.id}-comic-${index}`} className="chat-transcript-card">
                                <span className="chat-transcript-name">📚 Comic · {(comic.split('/').pop() || 'comic.json').replace(/\.json$/i, '').slice(0, 16)}</span>
                                <div className="chat-transcript-actions">
                                  <button
                                    type="button"
                                    disabled={app.editorOpening || !app.activeConversationID || !app.comicArtifactIDFromURL(comic)}
                                    title={app.comicArtifactIDFromURL(comic) ? 'Open this revision in the comic editor' : 'Persisted comic required'}
                                    onClick={() => {
                                      const artifactID = app.comicArtifactIDFromURL(comic);
                                      if (artifactID && app.activeConversationID) {
                                        void app.openComicEditor(app.activeConversationID, artifactID);
                                      }
                                    }}
                                  >
                                    Edit
                                  </button>
                                  <button
                                    type="button"
                                    onClick={() => app.saveGeneratedComic(comic, index)}
                                    disabled={!comic.startsWith('/atelier-artifact/')}
                                  >
                                    Download project
                                  </button>
                                </div>
                              </div>
                            ))}
                          </div>
                        ) : null}
                        {entry.thinking ? (
                          <div className="thinking-panel">
                            <button
                              className="thinking-toggle"
                              type="button"
                              aria-expanded={!thinkingCollapsed}
                              onClick={() => app.toggleThinkingCollapsed(entry.id)}
                            >
                              {thinkingCollapsed ? 'Show thinking' : 'Hide thinking'}
                            </button>
                            {thinkingCollapsed ? null : (
                              <div className="thinking markdown-body">
                                <ReactMarkdown remarkPlugins={[remarkGfm]}>
                                  {entry.thinking}
                                </ReactMarkdown>
                              </div>
                            )}
                          </div>
                        ) : null}
                        {/* Live activity line while the turn works but no response
                            text has arrived yet — names the harness phase instead
                            of a static "..." (see StreamingActivity). */}
                        {entry.role === 'assistant' && entry.streaming && !entry.content ? (
                          <StreamingActivity run={entry.harnessRun} />
                        ) : null}
                        {entry.role === 'assistant' || entry.role === 'system' ? (
                          <div className={`markdown-body${entry.streaming && entry.content ? ' streaming-live' : ''}`}>
                            <ReactMarkdown remarkPlugins={[remarkGfm]}>
                              {entry.content}
                            </ReactMarkdown>
                          </div>
                        ) : (
                          <p>{entry.content || (entry.streaming ? '...' : '')}</p>
                        )}
                        {(entry.role === 'user' || entry.role === 'assistant') && entry.content ? (
                          <button
                            className="message-copy-button"
                            type="button"
                            aria-label={entry.role === 'user' ? 'Copy prompt' : 'Copy agent response'}
                            title={app.copiedMessageID === entry.id ? 'Copied' : entry.role === 'user' ? 'Copy prompt' : 'Copy response'}
                            onClick={() => app.copyMessage(entry)}
                          >
                            {app.copiedMessageID === entry.id ? '✓' : '⧉'}
                          </button>
                        ) : null}
                        {entry.error ? (
                          <div className="message-actions">
                            <button
                              className="message-retry-button"
                              type="button"
                              disabled={Boolean(app.activeStream)}
                              aria-label="Retry last turn"
                              title="Retry last turn"
                              onClick={() => app.retryFailedTurn(entry.id)}
                            >
                              ↻ Retry
                            </button>
                          </div>
                        ) : null}
                        {entry.role === 'assistant' && entry.harnessRun ? <TurnUsage run={entry.harnessRun} /> : null}
                        {entry.error ? <div className="error">{entry.error}</div> : null}
                      </article>
                    );
                  })}
                </div>

                <div
                  ref={app.composerRef}
                  className={`composer${app.composerDragging ? ' composer--dragging' : ''}`}
                  onDragEnter={app.handleComposerDragEnter}
                  onDragOver={app.handleComposerDragOver}
                  onDragLeave={app.handleComposerDragLeave}
                  onDrop={app.handleComposerDrop}
                >
                  {app.composerDragging ? (
                    <div className="composer-drop-overlay">Drop media to attach</div>
                  ) : null}
                  {asArray(app.stagingAttachments).length ? (
                    <div className="composer-staging">
                      {asArray(app.stagingAttachments).map((item) => (
                        <span key={item.key} className="composer-staging-row">
                          <span aria-hidden="true">{item.kind === 'audio' ? '♪' : '▶'}</span>
                          Staging {item.name}… {Math.round(item.fraction * 100)}%
                        </span>
                      ))}
                    </div>
                  ) : null}
                  {asArray(app.attachments).length ? (
                    <div className="attachment-strip">
                      {asArray(app.attachments).map((item) => (
                        <button
                          key={item.name}
                          onClick={() => {
                            // A staged upload that never rides a turn would
                            // otherwise live out the sweep window on disk.
                            if (item.stagedPath) {
                              void DiscardMediaUpload(item.stagedPath).catch(() => {});
                            }
                            app.setAttachments((items) => items.filter((next) => next.name !== item.name));
                          }}
                        >
                          {item.kind === 'audio' ? (
                            <span className="attachment-audio-chip" aria-hidden="true">
                              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                                <path d="M3 18v-6a9 9 0 0 1 18 0v6" />
                                <path d="M21 19a2 2 0 0 1-2 2h-1a2 2 0 0 1-2-2v-3a2 2 0 0 1 2-2h3zM3 19a2 2 0 0 0 2 2h1a2 2 0 0 0 2-2v-3a2 2 0 0 0-2-2H3z" />
                              </svg>
                            </span>
                          ) : item.kind === 'video' ? (
                            <span className="attachment-video-chip" aria-hidden="true">
                              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                                <path d="m22 8-6 4 6 4V8Z" />
                                <rect width="14" height="12" x="2" y="6" rx="2" ry="2" />
                              </svg>
                            </span>
                          ) : item.kind === 'subtitle' ? (
                            <span className="attachment-subtitle-chip" aria-hidden="true">
                              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                                <rect width="18" height="14" x="3" y="5" rx="2" ry="2" />
                                <path d="M7 15h4" />
                                <path d="M13 15h4" />
                                <path d="M7 11h2" />
                                <path d="M11 11h6" />
                              </svg>
                            </span>
                          ) : item.kind === 'markdown' ? (
                            <span className="attachment-subtitle-chip" aria-hidden="true">
                              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                                <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z" />
                                <path d="M14 2v6h6" />
                                <path d="M8 13h8" />
                                <path d="M8 17h5" />
                              </svg>
                            </span>
                          ) : (
                            <img src={item.src} alt="" />
                          )}
                          <span>{item.name}</span>
                        </button>
                      ))}
                    </div>
                  ) : null}
                  {app.mentionOpen && app.mentionMatchesState.length ? (
                    <ul className="mention-list" role="listbox" ref={app.mentionListRef}>
                      {app.mentionMatchesState.map((item, index) => (
                        <li key={item.name} role="option" aria-selected={index === app.mentionIndex}>
                          <button
                            type="button"
                            className={index === app.mentionIndex ? 'mention-item active' : 'mention-item'}
                            onMouseDown={(event) => {
                              // mousedown (not click) fires before the textarea
                              // loses focus, so the accept caret-restore works.
                              event.preventDefault();
                              app.acceptMention(index);
                            }}
                            onMouseEnter={() => app.setMentionIndex(index)}
                          >
                            {item.kind === 'image' ? (
                              <img src={item.src} alt="" className="mention-thumb" />
                            ) : (
                              <span className="mention-icon" aria-hidden="true">
                                {item.kind === 'audio' ? '♪' : item.kind === 'subtitle' ? '💬' : item.kind === 'markdown' ? '📝' : '▶'}
                              </span>
                            )}
                            <span className="mention-name">{item.name}</span>
                            {item.hint ? <span className="mention-hint">{item.hint}</span> : null}
                          </button>
                        </li>
                      ))}
                    </ul>
                  ) : null}
                  <div className="composer-input">
                    {/* Mention chip mirror: renders the same prompt text behind
                        the textarea with resolved @-mentions styled as chips.
                        The mirror paints the input's background and its own
                        text is transparent; the real input stays the native
                        textarea above it, so caret/IME/selection are untouched
                        and only the chip decorations show through. Its
                        typographic and box properties must match
                        .composer-input textarea exactly or chips drift out of
                        alignment. */}
                    <div ref={app.chatPromptMirrorRef} className="composer-input-mirror" aria-hidden="true">
                      {app.renderMentionOverlay(app.prompt)}
                    </div>
                    <textarea
                      ref={app.chatPromptRef}
                      value={app.prompt}
                      onChange={app.handleChatPromptChange}
                      onKeyDown={app.handleChatPromptKeyDown}
                      onPaste={app.handleChatPromptPaste}
                      onScroll={app.handleChatPromptScroll}
                      placeholder="Prompt Atelier..."
                    />
                  </div>
                  <div className="composer-actions">
                    <div className="composer-actions-left">
                      <button
                        type="button"
                        className="composer-workspace-chip"
                        // The workspace is immutable once a conversation exists.
                        // For a new chat, clicking opens the folder picker to
                        // choose the per-conversation root before the first send.
                        disabled={Boolean(app.activeConversationID) || Boolean(app.activeStream)}
                        onClick={app.chooseDraftWorkspace}
                        aria-label={app.activeConversationID
                          ? `Workspace locked to ${app.displayedWorkspace}`
                          : 'Choose workspace for this conversation'}
                        title={app.activeConversationID
                          ? `Workspace locked to ${app.displayedWorkspace}`
                          : 'Choose workspace for this conversation'}
                      >
                        <svg className="workspace-chip-icon" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
                        </svg>
                        <code>{shortenHomePath(app.displayedWorkspace)}</code>
                      </button>
                      {app.composerProjectNames ? (
                        <span
                          className="composer-project-chip"
                          title={`Filed under ${app.composerProjectNames.libraryName}${app.composerProjectNames.projectName ? ` › ${app.composerProjectNames.projectName}` : ''} — the assets panel and @-mentions scope to the library`}
                        >
                          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2Z" />
                          </svg>
                          <code>{app.composerProjectNames.projectName || app.composerProjectNames.libraryName}</code>
                        </span>
                      ) : null}
                      <label className="file-button" aria-label="Attach file" title="Attach file">
                        {/* Inline icon keeps the composer row compact — the text
                            label was pushing the submit row onto a new line at
                            narrow widths. The title/aria-label preserve meaning.
                            A paperclip reads as generic attach (image or audio)
                            rather than implying image-only. */}
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48" />
                        </svg>
                        <input type="file" accept={composerAcceptAttribute} multiple onChange={(event) => app.addFiles(event.target.files)} />
                      </label>
                    </div>
                    <div className="composer-submit-row">
                      <div className="composer-model-switch">
                        <label className="model-inline" htmlFor="primary-provider">
                          <span>Provider{app.conversationPrimaryOverride ? ' · this chat' : ''}</span>
                          <div className="model-inline-control">
                            <select
                              id="primary-provider"
                              aria-label="Provider for next message"
                              value={app.composerProvider}
                              onChange={(event) => app.selectComposerProvider(event.target.value as ChatProviderID)}
                            >
                              <option value="ollama">Ollama</option>
                              <option value="openrouter" disabled={app.airplaneMode}>OpenRouter</option>
                              <option value="openai-compatible">OpenAI-compatible (local)</option>
                            </select>
                          </div>
                        </label>
                        <label className="model-inline" htmlFor="primary-model">
                          <span>Model</span>
                          <div className="model-inline-control">
                            <ModelCombobox
                              id="primary-model"
                              ariaLabel="Model for next message"
                              placeholder="Type to filter models..."
                              value={app.composerModel}
                              onChange={app.selectComposerModel}
                              options={app.composerModelOptions}
                              allowCustom={app.composerProvider === 'openai-compatible'}
                            />
                            {app.composerProvider === 'ollama' || app.composerProvider === 'openrouter' ? (
                              <ModelCapabilityLink
                                id="primary-model"
                                modelName={app.composerModel}
                                models={app.composerProvider === 'ollama' ? app.models : undefined}
                                openRouterModels={app.composerProvider === 'openrouter' ? app.openRouterModels : undefined}
                                openID={app.openCapabilityID}
                                setOpenID={app.setOpenCapabilityID}
                                variant="icon"
                              />
                            ) : null}
                          </div>
                        </label>
                      </div>
                      {app.activeStream ? (
                        <button className="danger" onClick={app.stopChat} aria-label="Stop generating" title="Stop">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" stroke="none" aria-hidden="true">
                            <rect x="5" y="5" width="14" height="14" rx="2" />
                          </svg>
                        </button>
                      ) : (
                        <button className="primary" onClick={app.submitChat} disabled={!app.prompt.trim() || !app.composerModel || !app.composerModelIsValid} aria-label="Send message" title="Send">
                          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M3.714 3.048a.498.498 0 0 0-.683.627l2.843 7.627a2 2 0 0 1 0 1.396l-2.842 7.627a.498.498 0 0 0 .682.627l18-8.5a.5.5 0 0 0 0-.904z" />
                            <path d="M6 12h16" />
                          </svg>
                        </button>
                      )}
                    </div>
                  </div>
                </div>
              </div>
            </>
    </>
  );
}
