import {useEffect, useMemo, useRef, useState} from 'react';
import './App.css';
import {
  CancelStream,
  CheckOllama,
  GenerateImage,
  ListModels,
  SaveImage,
  StreamChat,
} from '../wailsjs/go/main/App';
import {main} from '../wailsjs/go/models';
import {EventsOff, EventsOn} from '../wailsjs/runtime/runtime';

type Mode = 'chat' | 'image';

type ChatEntry = {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  thinking?: string;
  images?: string[];
  streaming?: boolean;
  error?: string;
};

type ChatChunk = {
  requestID: string;
  content?: string;
  thinking?: string;
  done: boolean;
  error?: string;
  model?: string;
  reason?: string;
  tokens?: number;
};

type Attachment = {
  name: string;
  src: string;
  payload: string;
};

const defaultBaseURL = 'http://localhost:11434';

function App() {
  const [baseURL, setBaseURL] = useState(defaultBaseURL);
  const [status, setStatus] = useState<main.OllamaStatus | null>(null);
  const [models, setModels] = useState<main.OllamaModel[]>([]);
  const [model, setModel] = useState('');
  const [imageModel, setImageModel] = useState('');
  const [mode, setMode] = useState<Mode>('chat');
  const [system, setSystem] = useState('You are Atelier, a precise local AI collaborator.');
  const [prompt, setPrompt] = useState('');
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [chat, setChat] = useState<ChatEntry[]>([]);
  const [activeStream, setActiveStream] = useState<string | null>(null);
  const [imagePrompt, setImagePrompt] = useState('A product-quality desktop app icon for Atelier, refined studio craft, local AI workstation');
  const [imageSize, setImageSize] = useState(768);
  const [imageSteps, setImageSteps] = useState(24);
  const [imageResult, setImageResult] = useState<main.ImageGenerateResponse | null>(null);
  const [imageError, setImageError] = useState('');
  const [imageSaveStatus, setImageSaveStatus] = useState('');
  const [imageBusy, setImageBusy] = useState(false);
  const transcriptRef = useRef<HTMLDivElement | null>(null);

  const assistantEntryID = activeStream ? `assistant-${activeStream}` : '';

  useEffect(() => {
    refreshOllama();
  }, []);

  useEffect(() => {
    const onChunk = (chunk: ChatChunk) => {
      setChat((entries) =>
        entries.map((entry) => {
          if (entry.id !== `assistant-${chunk.requestID}`) {
            return entry;
          }
          return {
            ...entry,
            content: `${entry.content}${chunk.content ?? ''}`,
            thinking: `${entry.thinking ?? ''}${chunk.thinking ?? ''}`,
            streaming: !chunk.done && !chunk.error,
            error: chunk.error,
          };
        }),
      );
      if (chunk.done || chunk.error) {
        setActiveStream((current) => current === chunk.requestID ? null : current);
      }
    };
    EventsOn('ollama:chat:chunk', onChunk);
    return () => EventsOff('ollama:chat:chunk');
  }, []);

  useEffect(() => {
    transcriptRef.current?.scrollTo({top: transcriptRef.current.scrollHeight, behavior: 'smooth'});
  }, [chat]);

  const modelOptions = useMemo(() => models.map((item) => item.name), [models]);

  async function refreshOllama() {
    const nextStatus = await CheckOllama(baseURL);
    setStatus(nextStatus);
    if (!nextStatus.online) {
      setModels([]);
      return;
    }
    const nextModels = await ListModels(baseURL);
    setModels(nextModels);
    const firstModel = nextModels[0]?.name ?? '';
    setModel((current) => current || firstModel);
    setImageModel((current) => current || firstModel);
  }

  async function submitChat() {
    const trimmed = prompt.trim();
    if (!trimmed || !model || activeStream) {
      return;
    }

    const userEntry: ChatEntry = {
      id: `user-${Date.now()}`,
      role: 'user',
      content: trimmed,
      images: attachments.map((item) => item.src),
    };
    const requestMessages: main.ChatMessage[] = [
      ...chat
        .filter((entry) => entry.role !== 'system' && entry.content)
        .map((entry) => ({
          role: entry.role,
          content: entry.content,
          images: entry.images?.map((image) => image.replace(/^data:image\/[a-z+.-]+;base64,/, '')),
        })),
      {
        role: 'user',
        content: trimmed,
        images: attachments.map((item) => item.payload),
      },
    ];

    setPrompt('');
    setAttachments([]);
    setChat((entries) => [
      ...entries,
      userEntry,
      {id: 'assistant-pending', role: 'assistant', content: '', streaming: true},
    ]);

    const requestID = await StreamChat(main.ChatRequest.createFrom({
      baseURL,
      model,
      system,
      messages: requestMessages,
    }));

    setActiveStream(requestID);
    setChat((entries) =>
      entries.map((entry) =>
        entry.id === 'assistant-pending' ? {...entry, id: `assistant-${requestID}`} : entry,
      ),
    );
  }

  function handleChatPromptKeyDown(event: React.KeyboardEvent<HTMLTextAreaElement>) {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault();
      submitChat();
    }
  }

  async function stopChat() {
    if (activeStream) {
      await CancelStream(activeStream);
      setActiveStream(null);
      setChat((entries) =>
        entries.map((entry) =>
          entry.id === assistantEntryID ? {...entry, streaming: false, error: 'Stopped'} : entry,
        ),
      );
    }
  }

  async function addImages(files: FileList | null) {
    if (!files) {
      return;
    }
    const next = await Promise.all(Array.from(files).map(readImageFile));
    setAttachments((items) => [...items, ...next]);
  }

  async function generateImage() {
    if (!imageModel || !imagePrompt.trim() || imageBusy) {
      return;
    }
    setImageBusy(true);
    setImageError('');
    setImageSaveStatus('');
    setImageResult(null);
    try {
      const result = await GenerateImage(main.ImageGenerateRequest.createFrom({
        baseURL,
        model: imageModel,
        prompt: imagePrompt.trim(),
        width: imageSize,
        height: imageSize,
        steps: imageSteps,
      }));
      setImageResult(result);
      if (!result.images?.length && !result.text) {
        setImageError('Ollama returned a response, but Atelier did not find image data in it.');
      }
    } catch (error) {
      setImageError(error instanceof Error ? error.message : String(error));
    } finally {
      setImageBusy(false);
    }
  }

  async function saveGeneratedImage(image: string, index: number) {
    setImageSaveStatus('');
    setImageError('');
    try {
      const path = await SaveImage(main.SaveImageRequest.createFrom({
        image,
        suggestedName: `atelier-${Date.now()}-${index + 1}`,
      }));
      if (path) {
        setImageSaveStatus(`Saved to ${path}`);
      }
    } catch (error) {
      setImageError(error instanceof Error ? error.message : String(error));
    }
  }

  return (
    <main className="shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="mark">A</div>
          <div>
            <h1>Atelier</h1>
            <p>Local AI harness</p>
          </div>
        </div>

        <div className="connection">
          <label htmlFor="base-url">Ollama endpoint</label>
          <div className="endpoint-row">
            <input id="base-url" value={baseURL} onChange={(event) => setBaseURL(event.target.value)} />
            <button onClick={refreshOllama}>Refresh</button>
          </div>
          <div className={status?.online ? 'status online' : 'status offline'}>
            <span />
            {status?.online ? `Online ${status.version ?? ''}` : status?.error ?? 'Not checked'}
          </div>
        </div>

        <div className="field">
          <label htmlFor="model">Chat model</label>
          <select id="model" value={model} onChange={(event) => setModel(event.target.value)}>
            {modelOptions.map((name) => <option key={name}>{name}</option>)}
          </select>
        </div>

        <div className="field">
          <label htmlFor="image-model">Image model</label>
          <select id="image-model" value={imageModel} onChange={(event) => setImageModel(event.target.value)}>
            {modelOptions.map((name) => <option key={name}>{name}</option>)}
          </select>
        </div>

        <div className="field">
          <label htmlFor="system">System</label>
          <textarea id="system" value={system} onChange={(event) => setSystem(event.target.value)} />
        </div>
      </aside>

      <section className="workspace">
        <div className="toolbar">
          <div className="segmented" role="tablist">
            <button className={mode === 'chat' ? 'active' : ''} onClick={() => setMode('chat')}>Chat</button>
            <button className={mode === 'image' ? 'active' : ''} onClick={() => setMode('image')}>Image</button>
          </div>
          <div className="model-count">{models.length} local models</div>
        </div>

        {mode === 'chat' ? (
          <div className="chat-panel">
            <div className="transcript" ref={transcriptRef}>
              {chat.length === 0 ? (
                <div className="empty-state">
                  <h2>Ask a model, attach an image, or stream a long answer.</h2>
                  <p>Atelier talks to Ollama directly through the local API.</p>
                </div>
              ) : chat.map((entry) => (
                <article key={entry.id} className={`message ${entry.role}`}>
                  <div className="message-meta">{entry.role}{entry.streaming ? ' streaming' : ''}</div>
                  {entry.images?.length ? (
                    <div className="thumb-row">
                      {entry.images.map((image) => <img key={image} src={image} alt="" />)}
                    </div>
                  ) : null}
                  {entry.thinking ? <pre className="thinking">{entry.thinking}</pre> : null}
                  <p>{entry.content || (entry.streaming ? '...' : '')}</p>
                  {entry.error ? <div className="error">{entry.error}</div> : null}
                </article>
              ))}
            </div>

            <div className="composer">
              {attachments.length ? (
                <div className="attachment-strip">
                  {attachments.map((item) => (
                    <button key={item.name} onClick={() => setAttachments((items) => items.filter((next) => next.name !== item.name))}>
                      <img src={item.src} alt="" />
                      <span>{item.name}</span>
                    </button>
                  ))}
                </div>
              ) : null}
              <textarea
                value={prompt}
                onChange={(event) => setPrompt(event.target.value)}
                onKeyDown={handleChatPromptKeyDown}
                placeholder="Prompt Atelier..."
              />
              <div className="composer-actions">
                <label className="file-button">
                  Attach image
                  <input type="file" accept="image/*" multiple onChange={(event) => addImages(event.target.files)} />
                </label>
                {activeStream ? (
                  <button className="danger" onClick={stopChat}>Stop</button>
                ) : (
                  <button className="primary" onClick={submitChat} disabled={!prompt.trim() || !model}>Send</button>
                )}
              </div>
            </div>
          </div>
        ) : (
          <div className="image-panel">
            <div className="image-controls">
              <label htmlFor="image-prompt">Prompt</label>
              <textarea id="image-prompt" value={imagePrompt} onChange={(event) => setImagePrompt(event.target.value)} />
              <div className="inline-fields">
                <label>
                  Size
                  <input type="number" min={256} step={64} value={imageSize} onChange={(event) => setImageSize(Number(event.target.value))} />
                </label>
                <label>
                  Steps
                  <input type="number" min={1} max={80} value={imageSteps} onChange={(event) => setImageSteps(Number(event.target.value))} />
                </label>
              </div>
              <button className="primary" onClick={generateImage} disabled={!imagePrompt.trim() || !imageModel || imageBusy}>
                {imageBusy ? 'Generating' : 'Generate'}
              </button>
            </div>
            <div className="image-output">
              {imageBusy ? (
                <div className="empty-state busy-state">
                  <div className="spinner" />
                  <h2>Generating image...</h2>
                  <p>Large local image models may take a minute on first load.</p>
                </div>
              ) : imageError ? (
                <div className="empty-state error-state">
                  <h2>Generation failed</h2>
                  <p>{imageError}</p>
                  {imageResult?.raw ? <pre>{summarizeRaw(imageResult.raw)}</pre> : null}
                </div>
              ) : imageResult?.images.length ? (
                <div className="generated-results">
                  {imageResult.images.map((image: string, index: number) => (
                    <figure key={image} className="generated-image">
                      <img src={image} alt="Generated result" />
                      <figcaption>
                        <button className="primary" onClick={() => saveGeneratedImage(image, index)}>Save image</button>
                      </figcaption>
                    </figure>
                  ))}
                  {imageSaveStatus ? <div className="save-status">{imageSaveStatus}</div> : null}
                </div>
              ) : imageResult?.text ? (
                <div className="raw-output">
                  <h2>Ollama returned text</h2>
                  <pre>{imageResult.text}</pre>
                  {imageResult.raw ? <pre>{summarizeRaw(imageResult.raw)}</pre> : null}
                </div>
              ) : (
                <div className="empty-state">
                  <h2>Image generation lands here.</h2>
                  <p>Use an Ollama image-generation model; Atelier calls `/api/generate` directly.</p>
                </div>
              )}
            </div>
          </div>
        )}
      </section>
    </main>
  );
}

function summarizeRaw(raw: string): string {
  return raw.length > 1200 ? `${raw.slice(0, 1200)}...` : raw;
}

async function readImageFile(file: File): Promise<Attachment> {
  const dataURL = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });

  return {
    name: file.name,
    src: dataURL,
    payload: dataURL.replace(/^data:image\/[a-z+.-]+;base64,/, ''),
  };
}

export default App;
