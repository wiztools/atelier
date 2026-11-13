import {CheckOllama} from '../../wailsjs/go/main/App';
import {defaultOllamaNumCtx, numCtxOptions, numCtxLabel, defaultMediaTimeoutSeconds} from '../modelDefaults';
import {asArray, shortenHomePath} from '../utils';
import {InfoHint, AirplaneHint} from '../ui/Hints';
import {ModelSelectionPanel} from '../settings/ModelSelectionPanel';
import {AppModel} from '../app/useAppModel';

export function SettingsView({app}: {app: AppModel}) {
  return (
    <>
            <>
              <div className="toolbar">
                <button className="back-button" onClick={() => app.setView('app')}>← Back</button>
                <div className="model-count">Saved to ~/.atelier/config.json</div>
              </div>
              <div className="settings-screen">
                <div className="settings-header">
                  <h2>Settings</h2>
                  <p>Providers, model defaults, local tools, and general preferences.</p>
                </div>

                <div className="settings-tabs" role="tablist">
                  <button
                    type="button"
                    role="tab"
                    aria-selected={app.settingsTab === 'providers'}
                    className={app.settingsTab === 'providers' ? 'active' : ''}
                    onClick={() => app.setSettingsTab('providers')}
                  >Providers</button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={app.settingsTab === 'models'}
                    className={app.settingsTab === 'models' ? 'active' : ''}
                    onClick={() => app.setSettingsTab('models')}
                  >Models</button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={app.settingsTab === 'tools'}
                    className={app.settingsTab === 'tools' ? 'active' : ''}
                    onClick={() => app.setSettingsTab('tools')}
                  >Local Tools</button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={app.settingsTab === 'general'}
                    className={app.settingsTab === 'general' ? 'active' : ''}
                    onClick={() => app.setSettingsTab('general')}
                  >General</button>
                </div>

                {app.settingsTab === 'providers' ? (
                <>
                <section className="settings-section">
                  <h3>
                    Airplane Mode{' '}
                    <InfoHint
                      label="About airplane mode"
                      text="While on, Atelier itself never contacts a cloud service: chat runs on local Ollama and OpenAI-compatible endpoints (loopback-verified), cloud image/video/audio generation is unavailable, transcription uses the local whisper CLI, the image editor's inpaint is disabled, and update checks stand down. run_command stays available — airplane mode is not a sandbox."
                    />
                  </h3>
                  <div className="settings-rows">
                    <label className="airplane-toggle">
                      <input
                        type="checkbox"
                        checked={app.airplaneMode}
                        onChange={(event) => app.setAirplaneMode(event.target.checked)}
                        aria-label="Airplane mode — block all cloud providers"
                      />
                      <span>Block all cloud providers — local models and tools only</span>
                    </label>
                  </div>
                </section>

                <div className="settings-group-label">Local servers</div>

                <section className="settings-section">
                  <h3>Ollama</h3>
                  <div className="connection">
                    <label htmlFor="base-url">Ollama endpoint</label>
                    <div className="endpoint-row">
                      <input id="base-url" value={app.baseURL} onChange={(event) => app.setBaseURL(event.target.value)} />
                      <button
                        type="button"
                        className={`icon-btn${app.refreshing ? ' spinning' : ''}`}
                        onClick={() => app.refreshOllama()}
                        disabled={app.refreshing}
                        aria-label="Refresh models"
                        title="Refresh models"
                      >
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                          <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                          <polyline points="21 4 21 9 16 9" />
                          <polyline points="3 20 3 15 8 15" />
                        </svg>
                      </button>
                    </div>
                    <div className={app.status?.online ? 'status online' : 'status offline'}>
                      <span />
                      {app.status?.online
                        ? `Online${app.status.version ? ` ${app.status.version}` : ''} — ${asArray(app.models).length} local models available`
                        : app.status ? 'Offline' : 'Not checked'}
                    </div>
                    {/* Ollama's own reachability reason: CheckOllama's error
                        when the endpoint is down, or the boot fallback's
                        refresh failure. Nothing else writes the field — config,
                        window-state, and key-write failures toast instead. */}
                    {app.status?.error ? <div className="status-error">{app.status.error}</div> : null}
                    <div className="field-label-row">
                      <label htmlFor="ollama-num-ctx">Context Window (num_ctx)</label>
                      <InfoHint
                        label="Context window guidance"
                        text="Sent on every call. Planning prompts carry the tool catalog (~6K tokens) before evidence — 16K or more is recommended for tool-heavy turns."
                      />
                    </div>
                    <select
                      id="ollama-num-ctx"
                      value={app.ollamaNumCtx}
                      onChange={(event) => app.setOllamaNumCtx(Number(event.target.value) || defaultOllamaNumCtx)}
                    >
                      {(numCtxOptions.includes(app.ollamaNumCtx) ? numCtxOptions : [...numCtxOptions, app.ollamaNumCtx].sort((a, b) => a - b)).map((value) => (
                        <option key={value} value={value}>{numCtxLabel(value)}</option>
                      ))}
                    </select>
                  </div>
                </section>

                <section className="settings-section">
                  <h3>OpenAI-compatible (local)</h3>
                  <div className="connection">
                    <div className="field-label-row">
                      <label htmlFor="openai-image-endpoint">Local image server endpoint</label>
                      <InfoHint
                        label="Local image server endpoint"
                        text="Any server speaking OpenAI's /v1/images/generations (LocalAI, a diffusers shim, ...). Used when Image Provider is set to OpenAI-compatible."
                      />
                    </div>
                    <div className="endpoint-row">
                      <input
                        id="openai-image-endpoint"
                        value={app.openaiCompatibleBaseURL}
                        onChange={(event) => app.setOpenaiCompatibleBaseURL(event.target.value)}
                      />
                      <button onClick={() => app.refreshOpenAICompatibleModels()}>Refresh</button>
                    </div>
                    <div className={app.openaiCompatibleStatus === 'ok' ? 'status online' : 'status offline'}>
                      <span />
                      {app.openaiCompatibleStatus === 'ok'
                        ? `Online — ${app.openaiCompatibleModels.length} model${app.openaiCompatibleModels.length === 1 ? '' : 's'} listed`
                        : app.openaiCompatibleStatus === 'error'
                          ? 'Server unreachable or no /v1/models — enter model ids manually in Models.'
                          : 'Not checked.'}
                    </div>
                  </div>
                  <div className="connection">
                    <label htmlFor="openai-image-key">API Key (optional)</label>
                    <div className="endpoint-row">
                      <input
                        id="openai-image-key"
                        type="password"
                        placeholder={app.openaiCompatibleHasKey ? 'Key saved — enter a new key to replace it' : 'Leave empty for a local server without auth'}
                        value={app.openaiCompatibleKeyInput}
                        onChange={(event) => app.setOpenaiCompatibleKeyInput(event.target.value)}
                      />
                      <button type="button" className="icon-btn" onClick={app.saveOpenAICompatibleKey} disabled={!app.openaiCompatibleKeyInput} aria-label="Save key" title="Save key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                          <polyline points="17 21 17 13 7 13 7 21" />
                          <polyline points="7 3 7 8 15 8" />
                        </svg>
                      </button>
                      {app.openaiCompatibleHasKey ? (
                        <button type="button" className="icon-btn" onClick={app.clearOpenAICompatibleKey} aria-label="Clear key" title="Clear key">
                          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                            <polyline points="3 6 5 6 21 6" />
                            <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                          </svg>
                        </button>
                      ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                    </div>
                    <div className={app.openaiCompatibleHasKey ? 'status online' : 'status offline'}>
                      <span />
                      {app.openaiCompatibleHasKey ? 'Bearer key saved' : 'No key — requests go unauthenticated.'}
                    </div>
                  </div>
                </section>

                <div className="settings-group-label">Cloud services</div>

                <section className={app.airplaneMode ? 'settings-section airplane-blocked' : 'settings-section'}>
                  <h3>
                    OpenRouter{' '}
                    {app.airplaneMode ? (
                      <AirplaneHint label="OpenRouter unavailable" text="Unavailable while airplane mode is on — turn it off above to configure." />
                    ) : null}
                  </h3>
                  <div className="connection">
                    <label htmlFor="openrouter-key">API Key</label>
                    <div className="endpoint-row">
                      <input
                        id="openrouter-key"
                        type="password"
                        placeholder={app.openRouterHasKey ? 'Key saved — enter a new key to replace it' : 'sk-or-...'}
                        value={app.openRouterAPIKeyInput}
                        onChange={(event) => app.setOpenRouterAPIKeyInput(event.target.value)}
                      />
                      <button type="button" className="icon-btn" onClick={app.saveOpenRouterKey} disabled={!app.openRouterAPIKeyInput} aria-label="Save key" title="Save key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                          <polyline points="17 21 17 13 7 13 7 21" />
                          <polyline points="7 3 7 8 15 8" />
                        </svg>
                      </button>
                      <button type="button" className={`icon-btn${app.openRouterStatus === 'connected' ? ' spinning' : ''}`} onClick={app.checkOpenRouterConnection} disabled={!app.openRouterHasKey} aria-label="Check connection" title="Check connection">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                          <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                          <polyline points="21 4 21 9 16 9" />
                          <polyline points="3 20 3 15 8 15" />
                        </svg>
                      </button>
                      {app.openRouterHasKey ? (
                        <button type="button" className="icon-btn" onClick={app.clearOpenRouterKey} aria-label="Clear key" title="Clear key">
                          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                            <polyline points="3 6 5 6 21 6" />
                            <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                          </svg>
                        </button>
                      ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                    </div>
                    <div className={app.openRouterStatus === 'connected' ? 'status online' : 'status offline'}>
                      <span />
                      {app.openRouterStatus === 'connected'
                        ? 'Connected'
                        : app.openRouterStatus === 'error'
                          ? `OpenRouter: ${app.openRouterError}`
                          : app.openRouterHasKey
                            ? 'API key saved — not checked'
                            : 'No key saved.'}
                    </div>
                  </div>
                </section>

                <section className={app.airplaneMode ? 'settings-section airplane-blocked' : 'settings-section'}>
                  <h3>
                    fal.ai{' '}
                    {app.airplaneMode ? (
                      <AirplaneHint label="fal.ai unavailable" text="Unavailable while airplane mode is on — turn it off above to configure." />
                    ) : null}
                  </h3>
                  <div className="connection">
                    <label htmlFor="fal-key">API Key</label>
                    <div className="endpoint-row">
                      <input
                        id="fal-key"
                        type="password"
                        placeholder={app.falHasKey ? 'Key saved — enter a new key to replace it' : 'fal-...'}
                        value={app.falAPIKeyInput}
                        onChange={(event) => app.setFalAPIKeyInput(event.target.value)}
                      />
                      <button type="button" className="icon-btn" onClick={app.saveFalKey} disabled={!app.falAPIKeyInput} aria-label="Save key" title="Save key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                          <polyline points="17 21 17 13 7 13 7 21" />
                          <polyline points="7 3 7 8 15 8" />
                        </svg>
                      </button>
                      <button type="button" className={`icon-btn${app.falStatus === 'connected' ? ' spinning' : ''}`} onClick={app.checkFalConnection} disabled={!app.falHasKey} aria-label="Check connection" title="Check connection">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                          <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                          <polyline points="21 4 21 9 16 9" />
                          <polyline points="3 20 3 15 8 15" />
                        </svg>
                      </button>
                      {app.falHasKey ? (
                        <button type="button" className="icon-btn" onClick={app.clearFalKey} aria-label="Clear key" title="Clear key">
                          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                            <polyline points="3 6 5 6 21 6" />
                            <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                          </svg>
                        </button>
                      ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                    </div>
                    <div className={app.falStatus === 'connected' ? 'status online' : 'status offline'}>
                      <span />
                      {app.falStatus === 'connected'
                        ? 'Connected'
                        : app.falStatus === 'error'
                          ? `fal.ai: ${app.falError}`
                          : app.falHasKey
                            ? 'API key saved — not checked'
                            : 'No key saved.'}
                    </div>
                  </div>
                </section>

                <section className={app.airplaneMode ? 'settings-section airplane-blocked' : 'settings-section'}>
                  <h3>
                    Replicate{' '}
                    {app.airplaneMode ? (
                      <AirplaneHint label="Replicate unavailable" text="Unavailable while airplane mode is on — turn it off above to configure." />
                    ) : null}
                  </h3>
                  <div className="connection">
                    <label htmlFor="replicate-key">API Token</label>
                    <div className="endpoint-row">
                      <input
                        id="replicate-key"
                        type="password"
                        placeholder={app.replicateHasKey ? 'Token saved — enter a new token to replace it' : 'r8_...'}
                        value={app.replicateAPIKeyInput}
                        onChange={(event) => app.setReplicateAPIKeyInput(event.target.value)}
                      />
                      <button type="button" className="icon-btn" onClick={app.saveReplicateKey} disabled={!app.replicateAPIKeyInput} aria-label="Save token" title="Save token">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                          <polyline points="17 21 17 13 7 13 7 21" />
                          <polyline points="7 3 7 8 15 8" />
                        </svg>
                      </button>
                      <button type="button" className={`icon-btn${app.replicateStatus === 'connected' ? ' spinning' : ''}`} onClick={app.checkReplicateConnection} disabled={!app.replicateHasKey} aria-label="Check connection" title="Check connection">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                          <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                          <polyline points="21 4 21 9 16 9" />
                          <polyline points="3 20 3 15 8 15" />
                        </svg>
                      </button>
                      {app.replicateHasKey ? (
                        <button type="button" className="icon-btn" onClick={app.clearReplicateKey} aria-label="Clear token" title="Clear token">
                          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                            <polyline points="3 6 5 6 21 6" />
                            <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                          </svg>
                        </button>
                      ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                    </div>
                    <div className={app.replicateStatus === 'connected' ? 'status online' : 'status offline'}>
                      <span />
                      {app.replicateStatus === 'connected'
                        ? 'Connected'
                        : app.replicateStatus === 'error'
                          ? `Replicate: ${app.replicateError}`
                          : app.replicateHasKey
                            ? 'API token saved — not checked'
                            : 'No token saved.'}
                    </div>
                  </div>
                </section>

                <section className={app.airplaneMode ? 'settings-section airplane-blocked' : 'settings-section'}>
                  <h3>
                    Runware{' '}
                    {app.airplaneMode ? (
                      <AirplaneHint label="Runware unavailable" text="Unavailable while airplane mode is on — turn it off above to configure." />
                    ) : null}
                  </h3>
                  <div className="connection">
                    <label htmlFor="runware-key">API Key</label>
                    <div className="endpoint-row">
                      <input
                        id="runware-key"
                        type="password"
                        placeholder={app.runwareHasKey ? 'Key saved — enter a new key to replace it' : 'Runware API key'}
                        value={app.runwareAPIKeyInput}
                        onChange={(event) => app.setRunwareAPIKeyInput(event.target.value)}
                      />
                      <button type="button" className="icon-btn" onClick={app.saveRunwareKey} disabled={!app.runwareAPIKeyInput} aria-label="Save key" title="Save key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                          <polyline points="17 21 17 13 7 13 7 21" />
                          <polyline points="7 3 7 8 15 8" />
                        </svg>
                      </button>
                      <button type="button" className={`icon-btn${app.runwareStatus === 'connected' ? ' spinning' : ''}`} onClick={app.checkRunwareConnection} disabled={!app.runwareHasKey} aria-label="Check connection" title="Check connection">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                          <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                          <polyline points="21 4 21 9 16 9" />
                          <polyline points="3 20 3 15 8 15" />
                        </svg>
                      </button>
                      {app.runwareHasKey ? (
                        <button type="button" className="icon-btn" onClick={app.clearRunwareKey} aria-label="Clear key" title="Clear key">
                          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                            <polyline points="3 6 5 6 21 6" />
                            <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                          </svg>
                        </button>
                      ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                    </div>
                    <div className={app.runwareStatus === 'connected' ? 'status online' : 'status offline'}>
                      <span />
                      {app.runwareStatus === 'connected'
                        ? 'Connected'
                        : app.runwareStatus === 'error'
                          ? `Runware: ${app.runwareError}`
                          : app.runwareHasKey
                            ? 'API key saved — not checked'
                            : 'No key saved.'}
                    </div>
                  </div>
                </section>

                </>
                ) : null}

                {app.settingsTab === 'tools' ? (
                <>
                <div className="tools-tab-intro">
                  <span>Binaries installed on this machine — Atelier detects them on the PATH automatically, so most of this stays untouched. Local tools keep working in airplane mode.</span>
                  <button type="button" onClick={app.redetectLocalTools} disabled={app.localToolsDetecting}>
                    {app.localToolsDetecting ? 'Detecting…' : 'Re-detect'}
                  </button>
                </div>

                <section className="settings-section">
                  <h3>
                    Video &amp; Audio Tools (FFmpeg){' '}
                    <InfoHint
                      label="Local video and audio tools"
                      text="ffmpeg powers the local video and audio tools in chat: screenshot or storyboard a clip, split, join (with crossfades), extract, replace, or remove audio, convert formats, change speed or shape, reverse playback, fade in or out, and burn subtitles. Detected automatically on PATH, or set an explicit binary below. A missing ffprobe only degrades some paths — joins always re-encode and audio extraction falls back to MP3."
                    />
                  </h3>
                  <div className="settings-rows">
                    <div className="two-column">
                      <div className="field">
                        <div className="field-label-row">
                          <label htmlFor="ffmpeg-binary">FFmpeg Binary</label>
                          {app.ffmpegStatus?.detail ? (
                            <InfoHint label="FFmpeg binary detection" text={app.ffmpegStatus.detail} />
                          ) : null}
                        </div>
                        <input
                          id="ffmpeg-binary"
                          value={app.ffmpegBinary}
                          onChange={(event) => app.setFfmpegBinary(event.target.value)}
                          placeholder={app.ffmpegStatus?.path || 'auto-detect on PATH: ffmpeg'}
                        />
                      </div>

                      <div className="field">
                        <div className="field-label-row">
                          <label htmlFor="ffprobe-binary">FFprobe Binary</label>
                          {app.ffprobeStatus?.detail ? (
                            <InfoHint label="FFprobe binary detection" text={app.ffprobeStatus.detail} />
                          ) : null}
                        </div>
                        <input
                          id="ffprobe-binary"
                          value={app.ffprobeBinary}
                          onChange={(event) => app.setFfprobeBinary(event.target.value)}
                          placeholder={app.ffprobeStatus?.path || 'auto-detect on PATH: ffprobe'}
                        />
                      </div>
                    </div>
                    {!app.ffmpegStatus?.available ? (
                      <span className="hint">{app.ffmpegStatus?.detail ?? 'Detecting local tools…'}</span>
                    ) : null}
                  </div>
                </section>

                <section className="settings-section">
                  <h3>
                    Image Tools (sips + ImageMagick){' '}
                    <InfoHint
                      label="Local image tools"
                      text="Two local backends power the image-editing tools in chat. macOS's built-in sips converts formats (including HEIC photos), crops to an aspect, resizes, rotates, flips, and reports image facts — no install needed. ImageMagick adds watermarks and logo overlays, collages/grids, color adjustments (brightness, contrast, saturation, grayscale, sepia), and metadata stripping with webp output: `brew install imagemagick`. Both are detected automatically on PATH, or set explicit binaries below."
                    />
                  </h3>
                  <div className="settings-rows">
                    <div className="two-column">
                      <div className="field">
                        <div className="field-label-row">
                          <label htmlFor="sips-binary">sips Binary</label>
                          {app.sipsStatus?.detail ? (
                            <InfoHint label="sips binary detection" text={app.sipsStatus.detail} />
                          ) : null}
                        </div>
                        <input
                          id="sips-binary"
                          value={app.sipsBinary}
                          onChange={(event) => app.setSipsBinary(event.target.value)}
                          placeholder={app.sipsStatus?.path || 'auto-detect on PATH: sips'}
                        />
                      </div>

                      <div className="field">
                        <div className="field-label-row">
                          <label htmlFor="magick-binary">ImageMagick Binary</label>
                          {app.magickStatus?.detail ? (
                            <InfoHint label="ImageMagick binary detection" text={app.magickStatus.detail} />
                          ) : null}
                        </div>
                        <input
                          id="magick-binary"
                          value={app.magickBinary}
                          onChange={(event) => app.setMagickBinary(event.target.value)}
                          placeholder={app.magickStatus?.path || 'auto-detect on PATH: magick / convert'}
                        />
                      </div>
                    </div>
                    {!app.magickStatus?.available ? (
                      <span className="hint">{app.magickStatus?.detail ?? 'Detecting local tools…'}</span>
                    ) : null}
                  </div>
                </section>

                <section className="settings-section">
                  <h3>
                    Speech Tools (Whisper){' '}
                    <InfoHint
                      label="Local speech tools"
                      text="Whisper powers local transcription in chat — plain transcripts, timestamped VTT/SRT subtitles, and English translation of foreign-language speech. It runs when Transcription Provider is 'Whisper (local)' under Models. The binary is detected automatically on PATH ('whisper', then 'whisper-cli'); set an explicit binary, the model, or the optional VAD model below. The VAD model is whisper.cpp only: when set, silent stretches are skipped instead of transcribed as hallucinated text."
                    />
                  </h3>
                  <div className="settings-rows">
                    <div className="two-column">
                      <div className="field">
                        <div className="field-label-row">
                          <label htmlFor="whisper-binary">Whisper Binary</label>
                          {app.whisperStatus?.detail ? (
                            <InfoHint label="Whisper binary detection" text={app.whisperStatus.detail} />
                          ) : null}
                        </div>
                        <input
                          id="whisper-binary"
                          value={app.whisperBinary}
                          onChange={(event) => app.setWhisperBinary(event.target.value)}
                          placeholder={app.whisperStatus?.path || 'auto-detect on PATH: whisper, then whisper-cli'}
                        />
                      </div>

                      <div className="field">
                        <div className="field-label-row">
                          <label htmlFor="whisper-model">Whisper Model</label>
                          <InfoHint
                            label="Whisper model"
                            text={app.whisperStatus?.flavor === 'whisper-cpp'
                              ? 'A ggml model file for whisper.cpp (a leading ~/ is expanded). Empty resolves: $WHISPER_MODEL (if set when Atelier launched) → ~/.whisper-base.en.bin → ~/.cache/whisper.cpp/ggml-base.en.bin → ~/models/ggml-base.en.bin.'
                              : 'An openai-whisper model size (tiny, base, small, medium, large-v3, ...), downloaded on first use. Empty uses the CLI\'s own default, small. With whisper.cpp this becomes a ggml model file path instead.'}
                          />
                        </div>
                        <input
                          id="whisper-model"
                          value={app.whisperModel}
                          onChange={(event) => app.setWhisperModel(event.target.value)}
                          placeholder={app.whisperStatus?.flavor === 'whisper-cpp' ? '/path/to/ggml-base.en.bin' : 'small (whisper default)'}
                        />
                      </div>

                      <div className="field">
                        <div className="field-label-row">
                          <label htmlFor="whisper-vad-model">Whisper VAD Model</label>
                          <InfoHint
                            label="Optional voice-activity detection"
                            text="Path to a whisper.cpp VAD model (e.g. ~/.cache/whisper.cpp/ggml-silero-v6.2.0.bin). When set, local transcription runs with --vad so silent stretches are skipped instead of transcribed as hallucinated text. whisper.cpp only — the openai-whisper CLI has no VAD support and transcriptions carry a notice instead."
                          />
                        </div>
                        <input
                          id="whisper-vad-model"
                          value={app.whisperVADModel}
                          onChange={(event) => app.setWhisperVADModel(event.target.value)}
                          placeholder="optional: ~/.cache/whisper.cpp/ggml-silero-v6.2.0.bin"
                        />
                      </div>

                      <div className="field">
                        <div className="field-label-row">
                          <label htmlFor="whisper-chunk-minutes">Whisper Chunk Size (minutes)</label>
                          <InfoHint
                            label="Long-clip chunking"
                            text="For whisper.cpp: clips longer than this are transcoded to 16 kHz mono, cut into equal chunks with one ffmpeg pass, transcribed chunk by chunk, and stitched back together with offset timestamps — whisper.cpp's VAD misbehaves on long inputs (it can hallucinate one repeated line across the quiet stretches). Each chunk runs under its own timeout; a failed chunk is skipped with a notice. Empty or 0 disables chunking (single pass). whisper.cpp only; needs ffmpeg."
                          />
                        </div>
                        <input
                          id="whisper-chunk-minutes"
                          inputMode="numeric"
                          value={app.whisperChunkMinutes}
                          onChange={(event) => app.setWhisperChunkMinutes(event.target.value)}
                          placeholder="optional: e.g. 5"
                        />
                      </div>
                    </div>
                    {!app.whisperStatus?.available ? (
                      <span className="hint">{app.whisperStatus?.detail ?? 'Detecting local tools…'}</span>
                    ) : null}
                  </div>
                </section>
                </>
                ) : null}

                {app.settingsTab === 'general' ? (
                <>
                <section className="settings-section">
                  <h3>Storage</h3>
                  <div className="storage-list">
                    <div>
                      <span>Root</span>
                      <code>{shortenHomePath(app.storageConfig?.root ?? '~/.atelier')}</code>
                    </div>
                    <div>
                      <span>History</span>
                      <code>{shortenHomePath(app.storageConfig?.history ?? '~/.atelier/history')}</code>
                    </div>
                    <div>
                      <span>Default workspace</span>
                      <div className="workspace-picker">
                        <code>{shortenHomePath(app.toolConfig?.filesystem?.root ?? '~/Documents')}</code>
                        <button onClick={app.chooseToolWorkspace}>Choose</button>
                      </div>
                      <div className="storage-hint">
                        Used when a new conversation doesn't pick its own folder. Each conversation's workspace is locked at creation.
                      </div>
                    </div>
                  </div>
                  <div className="storage-actions">
                    <button className="danger" onClick={app.purgeArchivedConversations} disabled={app.purgeBusy}>
                      {app.purgeBusy ? 'Deleting...' : app.confirmPurgeArchived ? 'Confirm Delete' : 'Delete Archived Conversations'}
                    </button>
                    {app.confirmPurgeArchived && !app.purgeBusy ? (
                      <button onClick={() => app.setConfirmPurgeArchived(false)}>Cancel</button>
                    ) : null}
                  </div>
                  {app.confirmPurgeArchived ? (
                    <div className="storage-confirmation">
                      This permanently deletes archived conversations and local assets from ~/.atelier/history.
                    </div>
                  ) : null}
                </section>
                <section className="settings-section">
                  <h3>Updates</h3>
                  <div className="storage-actions">
                    <button onClick={app.checkForUpdates} disabled={app.updateCheckBusy}>
                      {app.updateCheckBusy ? 'Checking...' : 'Check for Updates'}
                    </button>
                  </div>
                  <div className="storage-hint">
                    Atelier checks for updates automatically once a day and installs only when you choose to.
                  </div>
                </section>
                <section className="settings-section">
                  <h3>Media generation</h3>
                  <div className="field-label-row">
                    <label htmlFor="media-timeout">Generation timeout</label>
                    <InfoHint
                      label="What the timeout covers"
                      text="Bounds each cloud media generation (image, video, audio, upscale, and the video transforms — fal.ai, Replicate, or Runware) from submit to delivered file, so an unavailable provider can't hold a turn open indefinitely. On timeout, a job that finished anyway is still delivered; one still running is cancelled so it stops billing. Local tools (ffmpeg, whisper) are unaffected."
                    />
                  </div>
                  <div className="endpoint-row">
                    <input
                      id="media-timeout"
                      type="number"
                      min={1}
                      max={240}
                      value={app.mediaTimeoutMinutes}
                      onChange={(event) => app.setMediaTimeoutMinutes(Math.min(240, Math.max(1, Math.round(Number(event.target.value) || Math.round(defaultMediaTimeoutSeconds / 60)))))}
                    />
                    <span className="hint">minutes</span>
                  </div>
                </section>
                </>
                ) : null}

                {app.settingsTab === 'models' ? (
                <>
                <ModelSelectionPanel
                  variant="settings"
                  value={app.globalModelSelection}
                  onChange={app.patchSettingsModelSelection}
                  catalogs={app.modelSelectionCatalogs}
                  durationOptions={{video: app.videoDurationOptions, image: app.videoDurationImageOptions, keyframe: app.videoDurationKeyframeOptions, extend: app.videoDurationExtendOptions, sound: app.soundDurationOptions}}
                  airplaneMode={app.airplaneMode}
                  onOpenLocalTools={() => app.setSettingsTab('tools')}
                />
                </>
                ) : null}

                {app.settingsTab === 'general' ? (
                <section className="settings-section">
                  <h3>System</h3>
                  <div className="field">
                    <textarea id="system" aria-label="System" value={app.system} onChange={(event) => app.setSystem(event.target.value)} />
                  </div>
                </section>
                ) : null}
              </div>
            </>
    </>
  );
}
