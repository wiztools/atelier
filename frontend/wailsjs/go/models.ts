export namespace main {
	
	export class ConfigUpdates {
	    manifestUrl: string;
	    autoCheck?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConfigUpdates(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.manifestUrl = source["manifestUrl"];
	        this.autoCheck = source["autoCheck"];
	    }
	}
	export class ConfigUI {
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigUI(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	    }
	}
	export class ConfigFilesystemTool {
	    root: string;
	    maxOutputBytes: number;
	    timeoutMs: number;
	    allowedCommands: string[];
	
	    static createFrom(source: any = {}) {
	        return new ConfigFilesystemTool(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.root = source["root"];
	        this.maxOutputBytes = source["maxOutputBytes"];
	        this.timeoutMs = source["timeoutMs"];
	        this.allowedCommands = source["allowedCommands"];
	    }
	}
	export class ConfigTools {
	    filesystem: ConfigFilesystemTool;
	
	    static createFrom(source: any = {}) {
	        return new ConfigTools(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filesystem = this.convertValues(source["filesystem"], ConfigFilesystemTool);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigAudioGeneration {
	    duration: string;
	    extendDuration: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigAudioGeneration(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.duration = source["duration"];
	        this.extendDuration = source["extendDuration"];
	    }
	}
	export class ConfigVideoGeneration {
	    duration: string;
	    aspectRatio: string;
	    resolution?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigVideoGeneration(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.duration = source["duration"];
	        this.aspectRatio = source["aspectRatio"];
	        this.resolution = source["resolution"];
	    }
	}
	export class ConfigImageGeneration {
	    aspectRatio?: string;
	    sizePreset?: string;
	    steps: number;
	    width?: number;
	    height?: number;
	
	    static createFrom(source: any = {}) {
	        return new ConfigImageGeneration(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.aspectRatio = source["aspectRatio"];
	        this.sizePreset = source["sizePreset"];
	        this.steps = source["steps"];
	        this.width = source["width"];
	        this.height = source["height"];
	    }
	}
	export class ConfigGeneration {
	    image: ConfigImageGeneration;
	    video: ConfigVideoGeneration;
	    audio: ConfigAudioGeneration;
	    mediaTimeoutSeconds?: number;
	
	    static createFrom(source: any = {}) {
	        return new ConfigGeneration(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.image = this.convertValues(source["image"], ConfigImageGeneration);
	        this.video = this.convertValues(source["video"], ConfigVideoGeneration);
	        this.audio = this.convertValues(source["audio"], ConfigAudioGeneration);
	        this.mediaTimeoutSeconds = source["mediaTimeoutSeconds"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigPrompts {
	    system: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigPrompts(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.system = source["system"];
	    }
	}
	export class ConfigModels {
	    primaryProvider?: string;
	    harnessProvider?: string;
	    imageProvider?: string;
	    videoProvider?: string;
	    transcriptionProvider?: string;
	    inpaintProvider?: string;
	    lipsyncProvider?: string;
	    speechProvider?: string;
	    audioProvider?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigModels(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.primaryProvider = source["primaryProvider"];
	        this.harnessProvider = source["harnessProvider"];
	        this.imageProvider = source["imageProvider"];
	        this.videoProvider = source["videoProvider"];
	        this.transcriptionProvider = source["transcriptionProvider"];
	        this.inpaintProvider = source["inpaintProvider"];
	        this.lipsyncProvider = source["lipsyncProvider"];
	        this.speechProvider = source["speechProvider"];
	        this.audioProvider = source["audioProvider"];
	    }
	}
	export class ConfigLocalImageMagick {
	    binary?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLocalImageMagick(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.binary = source["binary"];
	    }
	}
	export class ConfigLocalSips {
	    binary?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLocalSips(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.binary = source["binary"];
	    }
	}
	export class ConfigLocalFFprobe {
	    binary?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLocalFFprobe(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.binary = source["binary"];
	    }
	}
	export class ConfigLocalFFmpeg {
	    binary?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLocalFFmpeg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.binary = source["binary"];
	    }
	}
	export class ConfigLocalWhisper {
	    binary?: string;
	    model?: string;
	    vadModel?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLocalWhisper(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.binary = source["binary"];
	        this.model = source["model"];
	        this.vadModel = source["vadModel"];
	    }
	}
	export class ConfigLocalProviders {
	    whisper: ConfigLocalWhisper;
	    ffmpeg: ConfigLocalFFmpeg;
	    ffprobe: ConfigLocalFFprobe;
	    sips: ConfigLocalSips;
	    magick: ConfigLocalImageMagick;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLocalProviders(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.whisper = this.convertValues(source["whisper"], ConfigLocalWhisper);
	        this.ffmpeg = this.convertValues(source["ffmpeg"], ConfigLocalFFmpeg);
	        this.ffprobe = this.convertValues(source["ffprobe"], ConfigLocalFFprobe);
	        this.sips = this.convertValues(source["sips"], ConfigLocalSips);
	        this.magick = this.convertValues(source["magick"], ConfigLocalImageMagick);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigOpenAICompatible {
	    baseURL: string;
	    primary?: string;
	    harness?: string;
	    model?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigOpenAICompatible(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseURL = source["baseURL"];
	        this.primary = source["primary"];
	        this.harness = source["harness"];
	        this.model = source["model"];
	    }
	}
	export class ConfigReplicate {
	    enabled: boolean;
	    model?: string;
	    imageEditModel?: string;
	    videoModel?: string;
	    videoImageModel?: string;
	    videoExtendModel?: string;
	    upscaleModel?: string;
	    inpaintModel?: string;
	    videoUpscaleModel?: string;
	    videoRestyleModel?: string;
	    videoReframeModel?: string;
	    transcribeModel?: string;
	    lipsyncImageModel?: string;
	    lipsyncVideoModel?: string;
	    soundEffectsModel?: string;
	    soundEffectsVideoModel?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigReplicate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.model = source["model"];
	        this.imageEditModel = source["imageEditModel"];
	        this.videoModel = source["videoModel"];
	        this.videoImageModel = source["videoImageModel"];
	        this.videoExtendModel = source["videoExtendModel"];
	        this.upscaleModel = source["upscaleModel"];
	        this.inpaintModel = source["inpaintModel"];
	        this.videoUpscaleModel = source["videoUpscaleModel"];
	        this.videoRestyleModel = source["videoRestyleModel"];
	        this.videoReframeModel = source["videoReframeModel"];
	        this.transcribeModel = source["transcribeModel"];
	        this.lipsyncImageModel = source["lipsyncImageModel"];
	        this.lipsyncVideoModel = source["lipsyncVideoModel"];
	        this.soundEffectsModel = source["soundEffectsModel"];
	        this.soundEffectsVideoModel = source["soundEffectsVideoModel"];
	    }
	}
	export class ConfigFal {
	    enabled: boolean;
	    model?: string;
	    imageEditModel?: string;
	    videoModel?: string;
	    videoImageModel?: string;
	    videoExtendModel?: string;
	    videoMotionModel?: string;
	    videoKeyframeModel?: string;
	    audioModel?: string;
	    soundEffectsModel?: string;
	    audioCloneModel?: string;
	    audioExtendModel?: string;
	    voiceDesignModel?: string;
	    transcribeModel?: string;
	    upscaleModel?: string;
	    inpaintModel?: string;
	    videoUpscaleModel?: string;
	    videoReframeModel?: string;
	    videoRestyleModel?: string;
	    lipsyncImageModel?: string;
	    lipsyncVideoModel?: string;
	    soundEffectsVideoModel?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigFal(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.model = source["model"];
	        this.imageEditModel = source["imageEditModel"];
	        this.videoModel = source["videoModel"];
	        this.videoImageModel = source["videoImageModel"];
	        this.videoExtendModel = source["videoExtendModel"];
	        this.videoMotionModel = source["videoMotionModel"];
	        this.videoKeyframeModel = source["videoKeyframeModel"];
	        this.audioModel = source["audioModel"];
	        this.soundEffectsModel = source["soundEffectsModel"];
	        this.audioCloneModel = source["audioCloneModel"];
	        this.audioExtendModel = source["audioExtendModel"];
	        this.voiceDesignModel = source["voiceDesignModel"];
	        this.transcribeModel = source["transcribeModel"];
	        this.upscaleModel = source["upscaleModel"];
	        this.inpaintModel = source["inpaintModel"];
	        this.videoUpscaleModel = source["videoUpscaleModel"];
	        this.videoReframeModel = source["videoReframeModel"];
	        this.videoRestyleModel = source["videoRestyleModel"];
	        this.lipsyncImageModel = source["lipsyncImageModel"];
	        this.lipsyncVideoModel = source["lipsyncVideoModel"];
	        this.soundEffectsVideoModel = source["soundEffectsVideoModel"];
	    }
	}
	export class ConfigOpenRouter {
	    enabled: boolean;
	    primary?: string;
	    harness?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigOpenRouter(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.primary = source["primary"];
	        this.harness = source["harness"];
	    }
	}
	export class ConfigOllamaModels {
	    primary: string;
	    harness: string;
	    image: string;
	    speech?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigOllamaModels(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.primary = source["primary"];
	        this.harness = source["harness"];
	        this.image = source["image"];
	        this.speech = source["speech"];
	    }
	}
	export class ConfigOllama {
	    baseURL: string;
	    models: ConfigOllamaModels;
	    numCtx: number;
	
	    static createFrom(source: any = {}) {
	        return new ConfigOllama(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseURL = source["baseURL"];
	        this.models = this.convertValues(source["models"], ConfigOllamaModels);
	        this.numCtx = source["numCtx"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigProviders {
	    ollama: ConfigOllama;
	    openrouter: ConfigOpenRouter;
	    fal: ConfigFal;
	    replicate: ConfigReplicate;
	    openaiCompatible: ConfigOpenAICompatible;
	    local: ConfigLocalProviders;
	
	    static createFrom(source: any = {}) {
	        return new ConfigProviders(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ollama = this.convertValues(source["ollama"], ConfigOllama);
	        this.openrouter = this.convertValues(source["openrouter"], ConfigOpenRouter);
	        this.fal = this.convertValues(source["fal"], ConfigFal);
	        this.replicate = this.convertValues(source["replicate"], ConfigReplicate);
	        this.openaiCompatible = this.convertValues(source["openaiCompatible"], ConfigOpenAICompatible);
	        this.local = this.convertValues(source["local"], ConfigLocalProviders);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigStorage {
	    root: string;
	    history: string;
	    artifacts: string;
	    libraries: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigStorage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.root = source["root"];
	        this.history = source["history"];
	        this.artifacts = source["artifacts"];
	        this.libraries = source["libraries"];
	    }
	}
	export class AppConfig {
	    version: number;
	    airplaneMode: boolean;
	    storage: ConfigStorage;
	    providers: ConfigProviders;
	    models: ConfigModels;
	    prompts: ConfigPrompts;
	    generation: ConfigGeneration;
	    tools: ConfigTools;
	    ui: ConfigUI;
	    updates: ConfigUpdates;
	
	    static createFrom(source: any = {}) {
	        return new AppConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.airplaneMode = source["airplaneMode"];
	        this.storage = this.convertValues(source["storage"], ConfigStorage);
	        this.providers = this.convertValues(source["providers"], ConfigProviders);
	        this.models = this.convertValues(source["models"], ConfigModels);
	        this.prompts = this.convertValues(source["prompts"], ConfigPrompts);
	        this.generation = this.convertValues(source["generation"], ConfigGeneration);
	        this.tools = this.convertValues(source["tools"], ConfigTools);
	        this.ui = this.convertValues(source["ui"], ConfigUI);
	        this.updates = this.convertValues(source["updates"], ConfigUpdates);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AudioTrimSegment {
	    startSeconds: number;
	    endSeconds: number;
	
	    static createFrom(source: any = {}) {
	        return new AudioTrimSegment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.startSeconds = source["startSeconds"];
	        this.endSeconds = source["endSeconds"];
	    }
	}
	export class AudioTrimSource {
	    durationSeconds: number;
	
	    static createFrom(source: any = {}) {
	        return new AudioTrimSource(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.durationSeconds = source["durationSeconds"];
	    }
	}
	export class AudioTrimParams {
	    version: number;
	    source: AudioTrimSource;
	    segments: AudioTrimSegment[];
	
	    static createFrom(source: any = {}) {
	        return new AudioTrimParams(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.source = this.convertValues(source["source"], AudioTrimSource);
	        this.segments = this.convertValues(source["segments"], AudioTrimSegment);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AudioEditSubmitRequest {
	    parentConversationId: string;
	    sourceArtifactId: string;
	    sessionConversationId?: string;
	    inputArtifactId?: string;
	    sourceDigest?: string;
	    trim?: AudioTrimParams;
	
	    static createFrom(source: any = {}) {
	        return new AudioEditSubmitRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.parentConversationId = source["parentConversationId"];
	        this.sourceArtifactId = source["sourceArtifactId"];
	        this.sessionConversationId = source["sessionConversationId"];
	        this.inputArtifactId = source["inputArtifactId"];
	        this.sourceDigest = source["sourceDigest"];
	        this.trim = this.convertValues(source["trim"], AudioTrimParams);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	export class ToolFunction {
	    name: string;
	    arguments: number[];
	
	    static createFrom(source: any = {}) {
	        return new ToolFunction(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.arguments = source["arguments"];
	    }
	}
	export class ToolCall {
	    type?: string;
	    function: ToolFunction;
	
	    static createFrom(source: any = {}) {
	        return new ToolCall(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.function = this.convertValues(source["function"], ToolFunction);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatMessage {
	    role: string;
	    content: string;
	    images?: string[];
	    audios?: string[];
	    videos?: string[];
	    subtitles?: string[];
	    markdowns?: string[];
	    tool_calls?: ToolCall[];
	
	    static createFrom(source: any = {}) {
	        return new ChatMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.content = source["content"];
	        this.images = source["images"];
	        this.audios = source["audios"];
	        this.videos = source["videos"];
	        this.subtitles = source["subtitles"];
	        this.markdowns = source["markdowns"];
	        this.tool_calls = this.convertValues(source["tool_calls"], ToolCall);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConversationModelOverrides {
	    primaryProvider?: string;
	    primaryModel?: string;
	    harnessProvider?: string;
	    harnessModel?: string;
	    imageProvider?: string;
	    imageModel?: string;
	    videoProvider?: string;
	    videoModel?: string;
	    videoImageModel?: string;
	    imageEditModel?: string;
	    upscaleModel?: string;
	    inpaintProvider?: string;
	    inpaintModel?: string;
	    videoExtendModel?: string;
	    videoMotionModel?: string;
	    videoKeyframeModel?: string;
	    videoUpscaleModel?: string;
	    videoReframeModel?: string;
	    videoRestyleModel?: string;
	    speechProvider?: string;
	    audioModel?: string;
	    soundEffectsModel?: string;
	    soundEffectsVideoModel?: string;
	    audioCloneModel?: string;
	    audioExtendModel?: string;
	    voiceDesignModel?: string;
	    transcribeModel?: string;
	    lipsyncImageModel?: string;
	    lipsyncVideoModel?: string;
	    lipsyncProvider?: string;
	    audioProvider?: string;
	    transcriptionProvider?: string;
	    whisperModel?: string;
	    whisperBinary?: string;
	    whisperVADModel?: string;
	    imageAspectRatio?: string;
	    imageSizePreset?: string;
	    imageSteps?: number;
	    videoDuration?: string;
	    videoAspectRatio?: string;
	    videoResolution?: string;
	    soundDuration?: string;
	    audioExtendDuration?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConversationModelOverrides(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.primaryProvider = source["primaryProvider"];
	        this.primaryModel = source["primaryModel"];
	        this.harnessProvider = source["harnessProvider"];
	        this.harnessModel = source["harnessModel"];
	        this.imageProvider = source["imageProvider"];
	        this.imageModel = source["imageModel"];
	        this.videoProvider = source["videoProvider"];
	        this.videoModel = source["videoModel"];
	        this.videoImageModel = source["videoImageModel"];
	        this.imageEditModel = source["imageEditModel"];
	        this.upscaleModel = source["upscaleModel"];
	        this.inpaintProvider = source["inpaintProvider"];
	        this.inpaintModel = source["inpaintModel"];
	        this.videoExtendModel = source["videoExtendModel"];
	        this.videoMotionModel = source["videoMotionModel"];
	        this.videoKeyframeModel = source["videoKeyframeModel"];
	        this.videoUpscaleModel = source["videoUpscaleModel"];
	        this.videoReframeModel = source["videoReframeModel"];
	        this.videoRestyleModel = source["videoRestyleModel"];
	        this.speechProvider = source["speechProvider"];
	        this.audioModel = source["audioModel"];
	        this.soundEffectsModel = source["soundEffectsModel"];
	        this.soundEffectsVideoModel = source["soundEffectsVideoModel"];
	        this.audioCloneModel = source["audioCloneModel"];
	        this.audioExtendModel = source["audioExtendModel"];
	        this.voiceDesignModel = source["voiceDesignModel"];
	        this.transcribeModel = source["transcribeModel"];
	        this.lipsyncImageModel = source["lipsyncImageModel"];
	        this.lipsyncVideoModel = source["lipsyncVideoModel"];
	        this.lipsyncProvider = source["lipsyncProvider"];
	        this.audioProvider = source["audioProvider"];
	        this.transcriptionProvider = source["transcriptionProvider"];
	        this.whisperModel = source["whisperModel"];
	        this.whisperBinary = source["whisperBinary"];
	        this.whisperVADModel = source["whisperVADModel"];
	        this.imageAspectRatio = source["imageAspectRatio"];
	        this.imageSizePreset = source["imageSizePreset"];
	        this.imageSteps = source["imageSteps"];
	        this.videoDuration = source["videoDuration"];
	        this.videoAspectRatio = source["videoAspectRatio"];
	        this.videoResolution = source["videoResolution"];
	        this.soundDuration = source["soundDuration"];
	        this.audioExtendDuration = source["audioExtendDuration"];
	    }
	}
	export class ChatRequest {
	    requestID?: string;
	    conversationId?: string;
	    baseURL?: string;
	    provider?: string;
	    model: string;
	    selectedModel?: string;
	    system?: string;
	    messages: ChatMessage[];
	    think?: any;
	    options?: Record<string, any>;
	    format?: any;
	    tools?: any[];
	    workspace?: string;
	    projectId?: string;
	    modelOverrides?: ConversationModelOverrides;
	    referencedAssetIds?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ChatRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requestID = source["requestID"];
	        this.conversationId = source["conversationId"];
	        this.baseURL = source["baseURL"];
	        this.provider = source["provider"];
	        this.model = source["model"];
	        this.selectedModel = source["selectedModel"];
	        this.system = source["system"];
	        this.messages = this.convertValues(source["messages"], ChatMessage);
	        this.think = source["think"];
	        this.options = source["options"];
	        this.format = source["format"];
	        this.tools = source["tools"];
	        this.workspace = source["workspace"];
	        this.projectId = source["projectId"];
	        this.modelOverrides = this.convertValues(source["modelOverrides"], ConversationModelOverrides);
	        this.referencedAssetIds = source["referencedAssetIds"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatStreamStart {
	    requestID: string;
	    conversationId: string;
	    userVideos?: string[];
	    userSubtitles?: string[];
	    userMarkdowns?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ChatStreamStart(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requestID = source["requestID"];
	        this.conversationId = source["conversationId"];
	        this.userVideos = source["userVideos"];
	        this.userSubtitles = source["userSubtitles"];
	        this.userMarkdowns = source["userMarkdowns"];
	    }
	}
	export class CompositionOriginalAudio {
	    gainDb?: number;
	    muted?: boolean;
	    detached?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CompositionOriginalAudio(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.gainDb = source["gainDb"];
	        this.muted = source["muted"];
	        this.detached = source["detached"];
	    }
	}
	export class CompositionTransition {
	    kind: string;
	    seconds?: number;
	    audioCrossfade?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CompositionTransition(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.seconds = source["seconds"];
	        this.audioCrossfade = source["audioCrossfade"];
	    }
	}
	export class CompositionMediaRef {
	    artifactId: string;
	    conversationId?: string;
	    path: string;
	    mimeType?: string;
	    kind: string;
	    durationSeconds: number;
	    width?: number;
	    height?: number;
	    fps?: number;
	    hasAudio?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CompositionMediaRef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.artifactId = source["artifactId"];
	        this.conversationId = source["conversationId"];
	        this.path = source["path"];
	        this.mimeType = source["mimeType"];
	        this.kind = source["kind"];
	        this.durationSeconds = source["durationSeconds"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.fps = source["fps"];
	        this.hasAudio = source["hasAudio"];
	    }
	}
	export class CompositionClip {
	    id: string;
	    ref: CompositionMediaRef;
	    inSeconds: number;
	    outSeconds: number;
	    transitionToNext?: CompositionTransition;
	    audio: CompositionOriginalAudio;
	
	    static createFrom(source: any = {}) {
	        return new CompositionClip(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.ref = this.convertValues(source["ref"], CompositionMediaRef);
	        this.inSeconds = source["inSeconds"];
	        this.outSeconds = source["outSeconds"];
	        this.transitionToNext = this.convertValues(source["transitionToNext"], CompositionTransition);
	        this.audio = this.convertValues(source["audio"], CompositionOriginalAudio);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CompositionDuck {
	    startSeconds: number;
	    endSeconds: number;
	    gainDb: number;
	
	    static createFrom(source: any = {}) {
	        return new CompositionDuck(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.startSeconds = source["startSeconds"];
	        this.endSeconds = source["endSeconds"];
	        this.gainDb = source["gainDb"];
	    }
	}
	export class CompositionSegment {
	    id: string;
	    ref: CompositionMediaRef;
	    inSeconds: number;
	    outSeconds: number;
	    startSeconds: number;
	    gainDb?: number;
	    fadeInSeconds?: number;
	    fadeOutSeconds?: number;
	    ducks: CompositionDuck[];
	    detachedFromClipId?: string;
	
	    static createFrom(source: any = {}) {
	        return new CompositionSegment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.ref = this.convertValues(source["ref"], CompositionMediaRef);
	        this.inSeconds = source["inSeconds"];
	        this.outSeconds = source["outSeconds"];
	        this.startSeconds = source["startSeconds"];
	        this.gainDb = source["gainDb"];
	        this.fadeInSeconds = source["fadeInSeconds"];
	        this.fadeOutSeconds = source["fadeOutSeconds"];
	        this.ducks = this.convertValues(source["ducks"], CompositionDuck);
	        this.detachedFromClipId = source["detachedFromClipId"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CompositionLane {
	    id: string;
	    name: string;
	    gainDb?: number;
	    muted?: boolean;
	    segments: CompositionSegment[];
	
	    static createFrom(source: any = {}) {
	        return new CompositionLane(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.gainDb = source["gainDb"];
	        this.muted = source["muted"];
	        this.segments = this.convertValues(source["segments"], CompositionSegment);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CompositionOutput {
	    width?: number;
	    height?: number;
	    fps?: number;
	    fit?: string;
	
	    static createFrom(source: any = {}) {
	        return new CompositionOutput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.width = source["width"];
	        this.height = source["height"];
	        this.fps = source["fps"];
	        this.fit = source["fit"];
	    }
	}
	export class CompositionDoc {
	    schemaVersion: number;
	    name?: string;
	    output: CompositionOutput;
	    video: CompositionClip[];
	    audioLanes: CompositionLane[];
	
	    static createFrom(source: any = {}) {
	        return new CompositionDoc(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.schemaVersion = source["schemaVersion"];
	        this.name = source["name"];
	        this.output = this.convertValues(source["output"], CompositionOutput);
	        this.video = this.convertValues(source["video"], CompositionClip);
	        this.audioLanes = this.convertValues(source["audioLanes"], CompositionLane);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	
	export class CompositionRenderOperation {
	    id: string;
	    mode: string;
	    status: string;
	    progress: number;
	    error?: string;
	    notices: string[];
	    resultUrl?: string;
	    resultPosterUrl?: string;
	    outputSeconds: number;
	    width?: number;
	    height?: number;
	    fps?: number;
	    audioOnly: boolean;
	    includeAudio: boolean;
	    exportArtifactId?: string;
	    rangeStartSeconds?: number;
	    rangeEndSeconds?: number;
	    soloLaneId?: string;
	    startedAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new CompositionRenderOperation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.mode = source["mode"];
	        this.status = source["status"];
	        this.progress = source["progress"];
	        this.error = source["error"];
	        this.notices = source["notices"];
	        this.resultUrl = source["resultUrl"];
	        this.resultPosterUrl = source["resultPosterUrl"];
	        this.outputSeconds = source["outputSeconds"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.fps = source["fps"];
	        this.audioOnly = source["audioOnly"];
	        this.includeAudio = source["includeAudio"];
	        this.exportArtifactId = source["exportArtifactId"];
	        this.rangeStartSeconds = source["rangeStartSeconds"];
	        this.rangeEndSeconds = source["rangeEndSeconds"];
	        this.soloLaneId = source["soloLaneId"];
	        this.startedAt = source["startedAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class CompositionRenderRequest {
	    conversationId: string;
	    artifactId?: string;
	    doc?: CompositionDoc;
	    mode: string;
	    startSeconds?: number;
	    endSeconds?: number;
	    soloLaneId?: string;
	    includeAudio?: boolean;
	    width?: number;
	    height?: number;
	    fps?: number;
	    crf?: number;
	
	    static createFrom(source: any = {}) {
	        return new CompositionRenderRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.artifactId = source["artifactId"];
	        this.doc = this.convertValues(source["doc"], CompositionDoc);
	        this.mode = source["mode"];
	        this.startSeconds = source["startSeconds"];
	        this.endSeconds = source["endSeconds"];
	        this.soloLaneId = source["soloLaneId"];
	        this.includeAudio = source["includeAudio"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.fps = source["fps"];
	        this.crf = source["crf"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class CompositionSourceInfo {
	    conversationId: string;
	    conversationTitle?: string;
	    artifactId?: string;
	    url?: string;
	    mimeType?: string;
	    doc: CompositionDoc;
	    missingRefs: string[];
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new CompositionSourceInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.conversationTitle = source["conversationTitle"];
	        this.artifactId = source["artifactId"];
	        this.url = source["url"];
	        this.mimeType = source["mimeType"];
	        this.doc = this.convertValues(source["doc"], CompositionDoc);
	        this.missingRefs = source["missingRefs"];
	        this.warnings = source["warnings"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	export class ConversationAsset {
	    id: string;
	    conversationId: string;
	    kind: string;
	    mimeType?: string;
	    url?: string;
	    width?: number;
	    height?: number;
	    originTurnId: string;
	    role: string;
	    createdAt?: string;
	    conversationTitle?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConversationAsset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.conversationId = source["conversationId"];
	        this.kind = source["kind"];
	        this.mimeType = source["mimeType"];
	        this.url = source["url"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.originTurnId = source["originTurnId"];
	        this.role = source["role"];
	        this.createdAt = source["createdAt"];
	        this.conversationTitle = source["conversationTitle"];
	    }
	}
	export class HistoryContent {
	    type: string;
	    text?: string;
	    artifactId?: string;
	    path?: string;
	    mimeType?: string;
	    width?: number;
	    height?: number;
	
	    static createFrom(source: any = {}) {
	        return new HistoryContent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.text = source["text"];
	        this.artifactId = source["artifactId"];
	        this.path = source["path"];
	        this.mimeType = source["mimeType"];
	        this.width = source["width"];
	        this.height = source["height"];
	    }
	}
	export class HistoryTurn {
	    schemaVersion: number;
	    id: string;
	    conversationId: string;
	    createdAt: string;
	    kind: string;
	    role: string;
	    model?: string;
	    provider?: string;
	    content: HistoryContent[];
	    request?: Record<string, any>;
	    providerResponse?: Record<string, any>;
	    deletedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryTurn(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.schemaVersion = source["schemaVersion"];
	        this.id = source["id"];
	        this.conversationId = source["conversationId"];
	        this.createdAt = source["createdAt"];
	        this.kind = source["kind"];
	        this.role = source["role"];
	        this.model = source["model"];
	        this.provider = source["provider"];
	        this.content = this.convertValues(source["content"], HistoryContent);
	        this.request = source["request"];
	        this.providerResponse = source["providerResponse"];
	        this.deletedAt = source["deletedAt"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class EditSessionMeta {
	    parentConversationId: string;
	    sourceTurnId?: string;
	    sourceArtifactId?: string;
	    sourceTitleSnapshot?: string;
	    mediaKind?: string;
	
	    static createFrom(source: any = {}) {
	        return new EditSessionMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.parentConversationId = source["parentConversationId"];
	        this.sourceTurnId = source["sourceTurnId"];
	        this.sourceArtifactId = source["sourceArtifactId"];
	        this.sourceTitleSnapshot = source["sourceTitleSnapshot"];
	        this.mediaKind = source["mediaKind"];
	    }
	}
	export class HistoryConversationStats {
	    turnCount: number;
	    artifactCount: number;
	
	    static createFrom(source: any = {}) {
	        return new HistoryConversationStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.turnCount = source["turnCount"];
	        this.artifactCount = source["artifactCount"];
	    }
	}
	export class HistoryDefaults {
	    chatModel?: string;
	    imageModel?: string;
	    system?: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryDefaults(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chatModel = source["chatModel"];
	        this.imageModel = source["imageModel"];
	        this.system = source["system"];
	    }
	}
	export class HistoryProvider {
	    id: string;
	    baseURL: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryProvider(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.baseURL = source["baseURL"];
	    }
	}
	export class HistoryConversation {
	    schemaVersion: number;
	    id: string;
	    kind: string;
	    title: string;
	    createdAt: string;
	    updatedAt: string;
	    deletedAt?: string;
	    provider: HistoryProvider;
	    defaults: HistoryDefaults;
	    stats: HistoryConversationStats;
	    workspace?: string;
	    projectId?: string;
	    modelOverrides?: ConversationModelOverrides;
	    editSession?: EditSessionMeta;
	
	    static createFrom(source: any = {}) {
	        return new HistoryConversation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.schemaVersion = source["schemaVersion"];
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.title = source["title"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	        this.deletedAt = source["deletedAt"];
	        this.provider = this.convertValues(source["provider"], HistoryProvider);
	        this.defaults = this.convertValues(source["defaults"], HistoryDefaults);
	        this.stats = this.convertValues(source["stats"], HistoryConversationStats);
	        this.workspace = source["workspace"];
	        this.projectId = source["projectId"];
	        this.modelOverrides = this.convertValues(source["modelOverrides"], ConversationModelOverrides);
	        this.editSession = this.convertValues(source["editSession"], EditSessionMeta);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConversationDetail {
	    conversation: HistoryConversation;
	    turns: HistoryTurn[];
	
	    static createFrom(source: any = {}) {
	        return new ConversationDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversation = this.convertValues(source["conversation"], HistoryConversation);
	        this.turns = this.convertValues(source["turns"], HistoryTurn);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class TurnMatch {
	    turnId: string;
	    role: string;
	    createdAt: string;
	    before: string;
	    match: string;
	    after: string;
	
	    static createFrom(source: any = {}) {
	        return new TurnMatch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.turnId = source["turnId"];
	        this.role = source["role"];
	        this.createdAt = source["createdAt"];
	        this.before = source["before"];
	        this.match = source["match"];
	        this.after = source["after"];
	    }
	}
	export class ConversationSummary {
	    id: string;
	    kind: string;
	    title: string;
	    createdAt: string;
	    updatedAt: string;
	    deletedAt?: string;
	    turnCount: number;
	    artifactCount: number;
	    workspace?: string;
	    projectId?: string;
	    modelOverrides?: ConversationModelOverrides;
	
	    static createFrom(source: any = {}) {
	        return new ConversationSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.title = source["title"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	        this.deletedAt = source["deletedAt"];
	        this.turnCount = source["turnCount"];
	        this.artifactCount = source["artifactCount"];
	        this.workspace = source["workspace"];
	        this.projectId = source["projectId"];
	        this.modelOverrides = this.convertValues(source["modelOverrides"], ConversationModelOverrides);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConversationSearchResult {
	    conversation: ConversationSummary;
	    titleMatched: boolean;
	    matches: TurnMatch[];
	
	    static createFrom(source: any = {}) {
	        return new ConversationSearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversation = this.convertValues(source["conversation"], ConversationSummary);
	        this.titleMatched = source["titleMatched"];
	        this.matches = this.convertValues(source["matches"], TurnMatch);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class CropOperationParams {
	    x: number;
	    y: number;
	    width: number;
	    height: number;
	    sourceWidth: number;
	    sourceHeight: number;
	    aspectRatio?: string;
	
	    static createFrom(source: any = {}) {
	        return new CropOperationParams(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.x = source["x"];
	        this.y = source["y"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.sourceWidth = source["sourceWidth"];
	        this.sourceHeight = source["sourceHeight"];
	        this.aspectRatio = source["aspectRatio"];
	    }
	}
	export class DeleteLibraryResult {
	    deletedProjects: number;
	    deletedConversations: number;
	    deletedAssets: number;
	
	    static createFrom(source: any = {}) {
	        return new DeleteLibraryResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deletedProjects = source["deletedProjects"];
	        this.deletedConversations = source["deletedConversations"];
	        this.deletedAssets = source["deletedAssets"];
	    }
	}
	export class DeleteProjectResult {
	    deletedConversations: number;
	    deletedAssets: number;
	
	    static createFrom(source: any = {}) {
	        return new DeleteProjectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deletedConversations = source["deletedConversations"];
	        this.deletedAssets = source["deletedAssets"];
	    }
	}
	export class VideoTrimSegment {
	    startSeconds: number;
	    endSeconds: number;
	
	    static createFrom(source: any = {}) {
	        return new VideoTrimSegment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.startSeconds = source["startSeconds"];
	        this.endSeconds = source["endSeconds"];
	    }
	}
	export class VideoTrimParams {
	    version: number;
	    source: VideoReframeSource;
	    segments: VideoTrimSegment[];
	
	    static createFrom(source: any = {}) {
	        return new VideoTrimParams(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.source = this.convertValues(source["source"], VideoReframeSource);
	        this.segments = this.convertValues(source["segments"], VideoTrimSegment);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class VideoReframeMarker {
	    x: number;
	    y: number;
	    width: number;
	    height: number;
	    timeSeconds: number;
	    interpolationToNext: string;
	
	    static createFrom(source: any = {}) {
	        return new VideoReframeMarker(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.x = source["x"];
	        this.y = source["y"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.timeSeconds = source["timeSeconds"];
	        this.interpolationToNext = source["interpolationToNext"];
	    }
	}
	export class VideoReframeOutput {
	    width: number;
	    height: number;
	
	    static createFrom(source: any = {}) {
	        return new VideoReframeOutput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.width = source["width"];
	        this.height = source["height"];
	    }
	}
	export class VideoReframeSource {
	    width: number;
	    height: number;
	    durationSeconds: number;
	
	    static createFrom(source: any = {}) {
	        return new VideoReframeSource(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.width = source["width"];
	        this.height = source["height"];
	        this.durationSeconds = source["durationSeconds"];
	    }
	}
	export class VideoReframeParams {
	    version: number;
	    source: VideoReframeSource;
	    aspectRatio: string;
	    output: VideoReframeOutput;
	    markers: VideoReframeMarker[];
	
	    static createFrom(source: any = {}) {
	        return new VideoReframeParams(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.source = this.convertValues(source["source"], VideoReframeSource);
	        this.aspectRatio = source["aspectRatio"];
	        this.output = this.convertValues(source["output"], VideoReframeOutput);
	        this.markers = this.convertValues(source["markers"], VideoReframeMarker);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class InpaintOperationParams {
	    prompt?: string;
	    maskArtifactId?: string;
	    maskPath?: string;
	    maskWidth?: number;
	    maskHeight?: number;
	
	    static createFrom(source: any = {}) {
	        return new InpaintOperationParams(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.prompt = source["prompt"];
	        this.maskArtifactId = source["maskArtifactId"];
	        this.maskPath = source["maskPath"];
	        this.maskWidth = source["maskWidth"];
	        this.maskHeight = source["maskHeight"];
	    }
	}
	export class EditOperation {
	    id: string;
	    kind: string;
	    status: string;
	    createdAt?: string;
	    completedAt?: string;
	    inputArtifactId?: string;
	    resultArtifactId?: string;
	    resultPath?: string;
	    resultMimeType?: string;
	    resultWidth?: number;
	    resultHeight?: number;
	    resultDurationSeconds?: number;
	    resultUrl?: string;
	    provider?: string;
	    model?: string;
	    backend?: string;
	    costMicros?: number;
	    costUnknown?: boolean;
	    error?: string;
	    notices?: string[];
	    progress?: number;
	    inpaint?: InpaintOperationParams;
	    crop?: CropOperationParams;
	    reframe?: VideoReframeParams;
	    trim?: VideoTrimParams;
	    audioTrim?: AudioTrimParams;
	    adoptedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new EditOperation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.status = source["status"];
	        this.createdAt = source["createdAt"];
	        this.completedAt = source["completedAt"];
	        this.inputArtifactId = source["inputArtifactId"];
	        this.resultArtifactId = source["resultArtifactId"];
	        this.resultPath = source["resultPath"];
	        this.resultMimeType = source["resultMimeType"];
	        this.resultWidth = source["resultWidth"];
	        this.resultHeight = source["resultHeight"];
	        this.resultDurationSeconds = source["resultDurationSeconds"];
	        this.resultUrl = source["resultUrl"];
	        this.provider = source["provider"];
	        this.model = source["model"];
	        this.backend = source["backend"];
	        this.costMicros = source["costMicros"];
	        this.costUnknown = source["costUnknown"];
	        this.error = source["error"];
	        this.notices = source["notices"];
	        this.progress = source["progress"];
	        this.inpaint = this.convertValues(source["inpaint"], InpaintOperationParams);
	        this.crop = this.convertValues(source["crop"], CropOperationParams);
	        this.reframe = this.convertValues(source["reframe"], VideoReframeParams);
	        this.trim = this.convertValues(source["trim"], VideoTrimParams);
	        this.audioTrim = this.convertValues(source["audioTrim"], AudioTrimParams);
	        this.adoptedAt = source["adoptedAt"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class EditOperationState {
	    sessionConversationId: string;
	    createdSession: boolean;
	    operation: EditOperation;
	    sourceUrl?: string;
	
	    static createFrom(source: any = {}) {
	        return new EditOperationState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sessionConversationId = source["sessionConversationId"];
	        this.createdSession = source["createdSession"];
	        this.operation = this.convertValues(source["operation"], EditOperation);
	        this.sourceUrl = source["sourceUrl"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class EditSourceInfo {
	    conversationId: string;
	    conversationTitle?: string;
	    originTurnId?: string;
	    artifactId: string;
	    url: string;
	    mimeType?: string;
	    width?: number;
	    height?: number;
	    mediaKind?: string;
	    durationSeconds?: number;
	    sourceDigest?: string;
	    thumbnails?: string[];
	    notices?: string[];
	
	    static createFrom(source: any = {}) {
	        return new EditSourceInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.conversationTitle = source["conversationTitle"];
	        this.originTurnId = source["originTurnId"];
	        this.artifactId = source["artifactId"];
	        this.url = source["url"];
	        this.mimeType = source["mimeType"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.mediaKind = source["mediaKind"];
	        this.durationSeconds = source["durationSeconds"];
	        this.sourceDigest = source["sourceDigest"];
	        this.thumbnails = source["thumbnails"];
	        this.notices = source["notices"];
	    }
	}
	export class EditSessionState {
	    conversationId: string;
	    parentConversationId?: string;
	    parentTitle?: string;
	    parentAvailable: boolean;
	    parentStreaming: boolean;
	    source: EditSourceInfo;
	    operations: EditOperation[];
	    runningOperationId?: string;
	    inpaintProvider?: string;
	    inpaintFalModel?: string;
	    inpaintReplicateModel?: string;
	    falConfigured: boolean;
	    replicateConfigured: boolean;
	
	    static createFrom(source: any = {}) {
	        return new EditSessionState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.parentConversationId = source["parentConversationId"];
	        this.parentTitle = source["parentTitle"];
	        this.parentAvailable = source["parentAvailable"];
	        this.parentStreaming = source["parentStreaming"];
	        this.source = this.convertValues(source["source"], EditSourceInfo);
	        this.operations = this.convertValues(source["operations"], EditOperation);
	        this.runningOperationId = source["runningOperationId"];
	        this.inpaintProvider = source["inpaintProvider"];
	        this.inpaintFalModel = source["inpaintFalModel"];
	        this.inpaintReplicateModel = source["inpaintReplicateModel"];
	        this.falConfigured = source["falConfigured"];
	        this.replicateConfigured = source["replicateConfigured"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class EditorLaunchResult {
	    conversationId: string;
	    imageArtifactIds?: string[];
	    videoArtifactIds?: string[];
	    audioArtifactIds?: string[];
	    subtitleArtifactIds?: string[];
	    markdownArtifactIds?: string[];
	
	    static createFrom(source: any = {}) {
	        return new EditorLaunchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.imageArtifactIds = source["imageArtifactIds"];
	        this.videoArtifactIds = source["videoArtifactIds"];
	        this.audioArtifactIds = source["audioArtifactIds"];
	        this.subtitleArtifactIds = source["subtitleArtifactIds"];
	        this.markdownArtifactIds = source["markdownArtifactIds"];
	    }
	}
	export class EmptyStatePrompt {
	    heading: string;
	    sub: string;
	
	    static createFrom(source: any = {}) {
	        return new EmptyStatePrompt(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.heading = source["heading"];
	        this.sub = source["sub"];
	    }
	}
	export class FalModel {
	    id: string;
	    displayName: string;
	    category: string;
	    description: string;
	    status: string;
	    tags: string[];
	    thumbnailUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new FalModel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.category = source["category"];
	        this.description = source["description"];
	        this.status = source["status"];
	        this.tags = source["tags"];
	        this.thumbnailUrl = source["thumbnailUrl"];
	    }
	}
	export class HarnessToolCall {
	    name: string;
	    command?: string;
	    args?: string[];
	    cwd?: string;
	    env?: Record<string, string>;
	    timeoutMs?: number;
	    path?: string;
	    content?: string;
	    model?: string;
	    append?: boolean;
	    overwrite?: boolean;
	    maxBytes?: number;
	    allowBinary?: boolean;
	    negativePrompt?: string;
	    generateAudio?: boolean;
	    useVideoAs?: string;
	    source?: string;
	    imageRole?: string;
	    openingFrame?: string;
	    faceFrom?: string;
	    aspectRatio?: string;
	    duration?: string;
	    resolution?: string;
	    fps?: string;
	    loop?: boolean;
	    voice?: string;
	    style?: string;
	    emotion?: string;
	    direction?: string;
	    lyrics?: string;
	    cloneVoice?: boolean;
	    preserveSpeech?: boolean;
	    previewText?: string;
	    artifactId?: string;
	    scale?: string;
	    task?: string;
	    language?: string;
	    timestamps?: string;
	    at?: string;
	    count?: number;
	    start?: string;
	    end?: string;
	    mode?: string;
	    columns?: number;
	    transition?: string;
	    transitionDuration?: number;
	    format?: string;
	    quality?: number;
	    width?: number;
	    height?: number;
	    rotate?: number;
	    flip?: string;
	    speed?: number;
	    fadeIn?: number;
	    fadeOut?: number;
	    fadeTarget?: string;
	    position?: string;
	    opacity?: number;
	    brightness?: number;
	    contrast?: number;
	    saturation?: number;
	    grayscale?: boolean;
	    sepia?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new HarnessToolCall(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.cwd = source["cwd"];
	        this.env = source["env"];
	        this.timeoutMs = source["timeoutMs"];
	        this.path = source["path"];
	        this.content = source["content"];
	        this.model = source["model"];
	        this.append = source["append"];
	        this.overwrite = source["overwrite"];
	        this.maxBytes = source["maxBytes"];
	        this.allowBinary = source["allowBinary"];
	        this.negativePrompt = source["negativePrompt"];
	        this.generateAudio = source["generateAudio"];
	        this.useVideoAs = source["useVideoAs"];
	        this.source = source["source"];
	        this.imageRole = source["imageRole"];
	        this.openingFrame = source["openingFrame"];
	        this.faceFrom = source["faceFrom"];
	        this.aspectRatio = source["aspectRatio"];
	        this.duration = source["duration"];
	        this.resolution = source["resolution"];
	        this.fps = source["fps"];
	        this.loop = source["loop"];
	        this.voice = source["voice"];
	        this.style = source["style"];
	        this.emotion = source["emotion"];
	        this.direction = source["direction"];
	        this.lyrics = source["lyrics"];
	        this.cloneVoice = source["cloneVoice"];
	        this.preserveSpeech = source["preserveSpeech"];
	        this.previewText = source["previewText"];
	        this.artifactId = source["artifactId"];
	        this.scale = source["scale"];
	        this.task = source["task"];
	        this.language = source["language"];
	        this.timestamps = source["timestamps"];
	        this.at = source["at"];
	        this.count = source["count"];
	        this.start = source["start"];
	        this.end = source["end"];
	        this.mode = source["mode"];
	        this.columns = source["columns"];
	        this.transition = source["transition"];
	        this.transitionDuration = source["transitionDuration"];
	        this.format = source["format"];
	        this.quality = source["quality"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.rotate = source["rotate"];
	        this.flip = source["flip"];
	        this.speed = source["speed"];
	        this.fadeIn = source["fadeIn"];
	        this.fadeOut = source["fadeOut"];
	        this.fadeTarget = source["fadeTarget"];
	        this.position = source["position"];
	        this.opacity = source["opacity"];
	        this.brightness = source["brightness"];
	        this.contrast = source["contrast"];
	        this.saturation = source["saturation"];
	        this.grayscale = source["grayscale"];
	        this.sepia = source["sepia"];
	    }
	}
	export class HarnessToolResult {
	    name: string;
	    status: string;
	    summary: string;
	    result?: any;
	    error?: string;
	    notices?: string[];
	    diagnostics?: string[];
	
	    static createFrom(source: any = {}) {
	        return new HarnessToolResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.status = source["status"];
	        this.summary = source["summary"];
	        this.result = source["result"];
	        this.error = source["error"];
	        this.notices = source["notices"];
	        this.diagnostics = source["diagnostics"];
	    }
	}
	
	
	
	
	
	
	export class ImageEditSubmitRequest {
	    kind?: string;
	    parentConversationId: string;
	    sourceArtifactId: string;
	    sessionConversationId?: string;
	    inputArtifactId?: string;
	    prompt?: string;
	    maskPng?: string;
	    provider?: string;
	    model?: string;
	    crop?: CropOperationParams;
	    sourceDigest?: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageEditSubmitRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.parentConversationId = source["parentConversationId"];
	        this.sourceArtifactId = source["sourceArtifactId"];
	        this.sessionConversationId = source["sessionConversationId"];
	        this.inputArtifactId = source["inputArtifactId"];
	        this.prompt = source["prompt"];
	        this.maskPng = source["maskPng"];
	        this.provider = source["provider"];
	        this.model = source["model"];
	        this.crop = this.convertValues(source["crop"], CropOperationParams);
	        this.sourceDigest = source["sourceDigest"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class InpaintModelOption {
	    id: string;
	    label: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new InpaintModelOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.note = source["note"];
	    }
	}
	
	export class LibraryArchiveMissingRef {
	    conversationId: string;
	    turnId?: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new LibraryArchiveMissingRef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.turnId = source["turnId"];
	        this.path = source["path"];
	    }
	}
	export class LibraryExportPlan {
	    libraryId: string;
	    libraryName: string;
	    projects: number;
	    conversations: number;
	    assets: number;
	    bytes: number;
	    missingAssets?: LibraryArchiveMissingRef[];
	
	    static createFrom(source: any = {}) {
	        return new LibraryExportPlan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.libraryId = source["libraryId"];
	        this.libraryName = source["libraryName"];
	        this.projects = source["projects"];
	        this.conversations = source["conversations"];
	        this.assets = source["assets"];
	        this.bytes = source["bytes"];
	        this.missingAssets = this.convertValues(source["missingAssets"], LibraryArchiveMissingRef);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LibraryExportResult {
	    path: string;
	    conversations: number;
	    assets: number;
	    bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new LibraryExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.conversations = source["conversations"];
	        this.assets = source["assets"];
	        this.bytes = source["bytes"];
	    }
	}
	export class ProjectSummary {
	    id: string;
	    libraryId: string;
	    name: string;
	    notes?: string;
	    createdAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.libraryId = source["libraryId"];
	        this.name = source["name"];
	        this.notes = source["notes"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class LibrarySummary {
	    id: string;
	    name: string;
	    createdAt: string;
	    updatedAt: string;
	    projects: ProjectSummary[];
	
	    static createFrom(source: any = {}) {
	        return new LibrarySummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	        this.projects = this.convertValues(source["projects"], ProjectSummary);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LibraryImportResult {
	    library: LibrarySummary;
	    projects: number;
	    conversations: number;
	    assets: number;
	    bytes: number;
	    missingAssets: number;
	
	    static createFrom(source: any = {}) {
	        return new LibraryImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.library = this.convertValues(source["library"], LibrarySummary);
	        this.projects = source["projects"];
	        this.conversations = source["conversations"];
	        this.assets = source["assets"];
	        this.bytes = source["bytes"];
	        this.missingAssets = source["missingAssets"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class LocalBinaryStatus {
	    key: string;
	    label: string;
	    available: boolean;
	    flavor?: string;
	    path?: string;
	    source?: string;
	    detail?: string;
	
	    static createFrom(source: any = {}) {
	        return new LocalBinaryStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.available = source["available"];
	        this.flavor = source["flavor"];
	        this.path = source["path"];
	        this.source = source["source"];
	        this.detail = source["detail"];
	    }
	}
	export class LocalToolOverrides {
	    binaries?: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new LocalToolOverrides(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.binaries = source["binaries"];
	    }
	}
	export class LocalToolsReport {
	    binaries: LocalBinaryStatus[];
	    transcriptionProvider: string;
	
	    static createFrom(source: any = {}) {
	        return new LocalToolsReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.binaries = this.convertValues(source["binaries"], LocalBinaryStatus);
	        this.transcriptionProvider = source["transcriptionProvider"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MarkdownSourceInfo {
	    conversationId: string;
	    conversationTitle?: string;
	    artifactId: string;
	    url: string;
	    mimeType?: string;
	    format: string;
	    text: string;
	    wordCount: number;
	
	    static createFrom(source: any = {}) {
	        return new MarkdownSourceInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.conversationTitle = source["conversationTitle"];
	        this.artifactId = source["artifactId"];
	        this.url = source["url"];
	        this.mimeType = source["mimeType"];
	        this.format = source["format"];
	        this.text = source["text"];
	        this.wordCount = source["wordCount"];
	    }
	}
	export class ModelInfo {
	    provider: string;
	    id: string;
	    displayName: string;
	    contextLength?: number;
	    capabilities?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ModelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.contextLength = source["contextLength"];
	        this.capabilities = source["capabilities"];
	    }
	}
	export class OllamaModel {
	    name: string;
	    modifiedAt?: string;
	    size?: number;
	    family?: string;
	    parameter?: string;
	    capabilities?: string[];
	    imageGeneration?: boolean;
	    speechGeneration?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new OllamaModel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.modifiedAt = source["modifiedAt"];
	        this.size = source["size"];
	        this.family = source["family"];
	        this.parameter = source["parameter"];
	        this.capabilities = source["capabilities"];
	        this.imageGeneration = source["imageGeneration"];
	        this.speechGeneration = source["speechGeneration"];
	    }
	}
	export class OllamaStatus {
	    online: boolean;
	    version?: string;
	    baseURL: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new OllamaStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.online = source["online"];
	        this.version = source["version"];
	        this.baseURL = source["baseURL"];
	        this.error = source["error"];
	    }
	}
	
	export class PurgeArchivedResult {
	    deletedConversations: number;
	    deletedAssets: number;
	
	    static createFrom(source: any = {}) {
	        return new PurgeArchivedResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deletedConversations = source["deletedConversations"];
	        this.deletedAssets = source["deletedAssets"];
	    }
	}
	export class ReplicateModel {
	    id: string;
	    displayName: string;
	    category: string;
	    description: string;
	    status: string;
	    tags: string[];
	    thumbnailUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new ReplicateModel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.category = source["category"];
	        this.description = source["description"];
	        this.status = source["status"];
	        this.tags = source["tags"];
	        this.thumbnailUrl = source["thumbnailUrl"];
	    }
	}
	export class SaveAudioRequest {
	    path: string;
	    suggestedName?: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveAudioRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.suggestedName = source["suggestedName"];
	    }
	}
	export class SaveCompositionEditRequest {
	    conversationId: string;
	    sourceArtifactId?: string;
	    doc: CompositionDoc;
	
	    static createFrom(source: any = {}) {
	        return new SaveCompositionEditRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.sourceArtifactId = source["sourceArtifactId"];
	        this.doc = this.convertValues(source["doc"], CompositionDoc);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SaveCompositionEditResult {
	    artifactId: string;
	    url: string;
	    doc: CompositionDoc;
	    summary: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveCompositionEditResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.artifactId = source["artifactId"];
	        this.url = source["url"];
	        this.doc = this.convertValues(source["doc"], CompositionDoc);
	        this.summary = source["summary"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SaveDocumentExportRequest {
	    path: string;
	    format: string;
	    suggestedName?: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveDocumentExportRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.format = source["format"];
	        this.suggestedName = source["suggestedName"];
	    }
	}
	export class SaveImageRequest {
	    image: string;
	    suggestedName?: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveImageRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.image = source["image"];
	        this.suggestedName = source["suggestedName"];
	    }
	}
	export class SaveMarkdownEditRequest {
	    conversationId: string;
	    sourceArtifactId?: string;
	    format?: string;
	    text: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveMarkdownEditRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.sourceArtifactId = source["sourceArtifactId"];
	        this.format = source["format"];
	        this.text = source["text"];
	    }
	}
	export class SaveMarkdownEditResult {
	    artifactId: string;
	    url: string;
	    format: string;
	    wordCount: number;
	
	    static createFrom(source: any = {}) {
	        return new SaveMarkdownEditResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.artifactId = source["artifactId"];
	        this.url = source["url"];
	        this.format = source["format"];
	        this.wordCount = source["wordCount"];
	    }
	}
	export class SubtitleCue {
	    startSeconds: number;
	    endSeconds: number;
	    text: string;
	
	    static createFrom(source: any = {}) {
	        return new SubtitleCue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.startSeconds = source["startSeconds"];
	        this.endSeconds = source["endSeconds"];
	        this.text = source["text"];
	    }
	}
	export class SaveSubtitleEditRequest {
	    conversationId: string;
	    sourceArtifactId?: string;
	    format: string;
	    cues: SubtitleCue[];
	
	    static createFrom(source: any = {}) {
	        return new SaveSubtitleEditRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.sourceArtifactId = source["sourceArtifactId"];
	        this.format = source["format"];
	        this.cues = this.convertValues(source["cues"], SubtitleCue);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SaveSubtitleEditResult {
	    artifactId: string;
	    url: string;
	    format: string;
	    cueCount: number;
	
	    static createFrom(source: any = {}) {
	        return new SaveSubtitleEditResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.artifactId = source["artifactId"];
	        this.url = source["url"];
	        this.format = source["format"];
	        this.cueCount = source["cueCount"];
	    }
	}
	export class SaveTranscriptRequest {
	    path: string;
	    suggestedName?: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveTranscriptRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.suggestedName = source["suggestedName"];
	    }
	}
	export class SaveVideoRequest {
	    path: string;
	    suggestedName?: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveVideoRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.suggestedName = source["suggestedName"];
	    }
	}
	export class SearchOptions {
	    caseSensitive: boolean;
	    regex: boolean;
	    includeThinking: boolean;
	    limit: number;
	
	    static createFrom(source: any = {}) {
	        return new SearchOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.caseSensitive = source["caseSensitive"];
	        this.regex = source["regex"];
	        this.includeThinking = source["includeThinking"];
	        this.limit = source["limit"];
	    }
	}
	export class SearchResponse {
	    results: ConversationSearchResult[];
	    truncated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SearchResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.results = this.convertValues(source["results"], ConversationSearchResult);
	        this.truncated = source["truncated"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SeedCompositionRequest {
	    conversationId: string;
	    name?: string;
	    artifactIds: string[];
	
	    static createFrom(source: any = {}) {
	        return new SeedCompositionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.name = source["name"];
	        this.artifactIds = source["artifactIds"];
	    }
	}
	export class SettingsState {
	    lastTab?: string;
	
	    static createFrom(source: any = {}) {
	        return new SettingsState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lastTab = source["lastTab"];
	    }
	}
	export class SidebarState {
	    chatsCollapsed?: boolean;
	    librariesCollapsed?: boolean;
	    expandedLibraries?: string[];
	    expandedProjects?: string[];
	
	    static createFrom(source: any = {}) {
	        return new SidebarState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chatsCollapsed = source["chatsCollapsed"];
	        this.librariesCollapsed = source["librariesCollapsed"];
	        this.expandedLibraries = source["expandedLibraries"];
	        this.expandedProjects = source["expandedProjects"];
	    }
	}
	export class StagedMediaUpload {
	    path: string;
	    url: string;
	    size: number;
	    mediaType: string;
	
	    static createFrom(source: any = {}) {
	        return new StagedMediaUpload(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.url = source["url"];
	        this.size = source["size"];
	        this.mediaType = source["mediaType"];
	    }
	}
	
	export class SubtitleSourceInfo {
	    conversationId: string;
	    conversationTitle?: string;
	    artifactId: string;
	    url: string;
	    mimeType?: string;
	    format: string;
	    cues: SubtitleCue[];
	    warnings?: string[];
	
	    static createFrom(source: any = {}) {
	        return new SubtitleSourceInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.conversationTitle = source["conversationTitle"];
	        this.artifactId = source["artifactId"];
	        this.url = source["url"];
	        this.mimeType = source["mimeType"];
	        this.format = source["format"];
	        this.cues = this.convertValues(source["cues"], SubtitleCue);
	        this.warnings = source["warnings"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ToolCommandRequest {
	    command: string;
	    args?: string[];
	    cwd?: string;
	    env?: Record<string, string>;
	    timeoutMs?: number;
	
	    static createFrom(source: any = {}) {
	        return new ToolCommandRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.command = source["command"];
	        this.args = source["args"];
	        this.cwd = source["cwd"];
	        this.env = source["env"];
	        this.timeoutMs = source["timeoutMs"];
	    }
	}
	export class ToolCommandResult {
	    command: string[];
	    cwd: string;
	    exitCode: number;
	    stdout?: string;
	    stderr?: string;
	    durationMs: number;
	    truncated?: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolCommandResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.command = source["command"];
	        this.cwd = source["cwd"];
	        this.exitCode = source["exitCode"];
	        this.stdout = source["stdout"];
	        this.stderr = source["stderr"];
	        this.durationMs = source["durationMs"];
	        this.truncated = source["truncated"];
	        this.error = source["error"];
	    }
	}
	export class ToolExecutionRequest {
	    name: string;
	    call: HarnessToolCall;
	    requestId?: string;
	    conversationId?: string;
	    source?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolExecutionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.call = this.convertValues(source["call"], HarnessToolCall);
	        this.requestId = source["requestId"];
	        this.conversationId = source["conversationId"];
	        this.source = source["source"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ToolFileEntry {
	    name: string;
	    path: string;
	    isDir: boolean;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.isDir = source["isDir"];
	        this.size = source["size"];
	    }
	}
	export class ToolFileListRequest {
	    path?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileListRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	    }
	}
	export class ToolFileListResult {
	    root: string;
	    path: string;
	    entries: ToolFileEntry[];
	
	    static createFrom(source: any = {}) {
	        return new ToolFileListResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.root = source["root"];
	        this.path = source["path"];
	        this.entries = this.convertValues(source["entries"], ToolFileEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ToolFileReadRequest {
	    path: string;
	    maxBytes?: number;
	    allowBinary?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileReadRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.maxBytes = source["maxBytes"];
	        this.allowBinary = source["allowBinary"];
	    }
	}
	export class ToolFileReadResult {
	    path: string;
	    content: string;
	    bytes: number;
	    truncated?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileReadResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.content = source["content"];
	        this.bytes = source["bytes"];
	        this.truncated = source["truncated"];
	    }
	}
	export class ToolFileWriteRequest {
	    path: string;
	    content: string;
	    append?: boolean;
	    overwrite?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileWriteRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.content = source["content"];
	        this.append = source["append"];
	        this.overwrite = source["overwrite"];
	    }
	}
	export class ToolFileWriteResult {
	    path: string;
	    bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileWriteResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.bytes = source["bytes"];
	    }
	}
	
	export class ToolPermissionDecision {
	    approved: boolean;
	    outcome: string;
	    waitMs: number;
	
	    static createFrom(source: any = {}) {
	        return new ToolPermissionDecision(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.approved = source["approved"];
	        this.outcome = source["outcome"];
	        this.waitMs = source["waitMs"];
	    }
	}
	
	export class UIState {
	    sidebar: SidebarState;
	    settings: SettingsState;
	
	    static createFrom(source: any = {}) {
	        return new UIState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sidebar = this.convertValues(source["sidebar"], SidebarState);
	        this.settings = this.convertValues(source["settings"], SettingsState);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class UpdateStatus {
	    state: string;
	    currentVersion: string;
	    latestVersion?: string;
	    notes?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.currentVersion = source["currentVersion"];
	        this.latestVersion = source["latestVersion"];
	        this.notes = source["notes"];
	        this.error = source["error"];
	    }
	}
	export class UsageChildRow {
	    id: string;
	    title: string;
	    turns: number;
	    toolCalls: number;
	    costMicros: number;
	    costUnknown?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UsageChildRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.turns = source["turns"];
	        this.toolCalls = source["toolCalls"];
	        this.costMicros = source["costMicros"];
	        this.costUnknown = source["costUnknown"];
	    }
	}
	export class UsageModelRow {
	    provider: string;
	    model: string;
	    calls: number;
	    promptTokens?: number;
	    completionTokens?: number;
	    mediaImages?: number;
	    mediaVideos?: number;
	    mediaAudios?: number;
	    costMicros?: number;
	    costUnknown?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UsageModelRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.model = source["model"];
	        this.calls = source["calls"];
	        this.promptTokens = source["promptTokens"];
	        this.completionTokens = source["completionTokens"];
	        this.mediaImages = source["mediaImages"];
	        this.mediaVideos = source["mediaVideos"];
	        this.mediaAudios = source["mediaAudios"];
	        this.costMicros = source["costMicros"];
	        this.costUnknown = source["costUnknown"];
	    }
	}
	export class UsageToolRow {
	    name: string;
	    calls: number;
	    failures?: number;
	    mediaImages?: number;
	    mediaVideos?: number;
	    mediaAudios?: number;
	    costMicros?: number;
	    costUnknown?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UsageToolRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.calls = source["calls"];
	        this.failures = source["failures"];
	        this.mediaImages = source["mediaImages"];
	        this.mediaVideos = source["mediaVideos"];
	        this.mediaAudios = source["mediaAudios"];
	        this.costMicros = source["costMicros"];
	        this.costUnknown = source["costUnknown"];
	    }
	}
	export class UsageReport {
	    scope: string;
	    conversationId?: string;
	    projectId?: string;
	    libraryId?: string;
	    title: string;
	    conversations: number;
	    turns: number;
	    costMicros: number;
	    costUnknown?: boolean;
	    modelCalls: number;
	    toolCalls: number;
	    failedToolCalls?: number;
	    mediaImages?: number;
	    mediaVideos?: number;
	    mediaAudios?: number;
	    models: UsageModelRow[];
	    tools: UsageToolRow[];
	    children?: UsageChildRow[];
	
	    static createFrom(source: any = {}) {
	        return new UsageReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scope = source["scope"];
	        this.conversationId = source["conversationId"];
	        this.projectId = source["projectId"];
	        this.libraryId = source["libraryId"];
	        this.title = source["title"];
	        this.conversations = source["conversations"];
	        this.turns = source["turns"];
	        this.costMicros = source["costMicros"];
	        this.costUnknown = source["costUnknown"];
	        this.modelCalls = source["modelCalls"];
	        this.toolCalls = source["toolCalls"];
	        this.failedToolCalls = source["failedToolCalls"];
	        this.mediaImages = source["mediaImages"];
	        this.mediaVideos = source["mediaVideos"];
	        this.mediaAudios = source["mediaAudios"];
	        this.models = this.convertValues(source["models"], UsageModelRow);
	        this.tools = this.convertValues(source["tools"], UsageToolRow);
	        this.children = this.convertValues(source["children"], UsageChildRow);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class VideoEditSubmitRequest {
	    parentConversationId: string;
	    sourceArtifactId: string;
	    sessionConversationId?: string;
	    inputArtifactId?: string;
	    sourceDigest?: string;
	    reframe?: VideoReframeParams;
	    trim?: VideoTrimParams;
	
	    static createFrom(source: any = {}) {
	        return new VideoEditSubmitRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.parentConversationId = source["parentConversationId"];
	        this.sourceArtifactId = source["sourceArtifactId"];
	        this.sessionConversationId = source["sessionConversationId"];
	        this.inputArtifactId = source["inputArtifactId"];
	        this.sourceDigest = source["sourceDigest"];
	        this.reframe = this.convertValues(source["reframe"], VideoReframeParams);
	        this.trim = this.convertValues(source["trim"], VideoTrimParams);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	

}

