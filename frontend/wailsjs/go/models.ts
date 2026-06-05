export namespace main {
	
	export class ChatMessage {
	    role: string;
	    content: string;
	    images?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ChatMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.content = source["content"];
	        this.images = source["images"];
	    }
	}
	export class ChatRequest {
	    requestID?: string;
	    baseURL?: string;
	    model: string;
	    system?: string;
	    messages: ChatMessage[];
	    think?: any;
	    options?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new ChatRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requestID = source["requestID"];
	        this.baseURL = source["baseURL"];
	        this.model = source["model"];
	        this.system = source["system"];
	        this.messages = this.convertValues(source["messages"], ChatMessage);
	        this.think = source["think"];
	        this.options = source["options"];
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
	export class ImageGenerateRequest {
	    baseURL?: string;
	    model: string;
	    prompt: string;
	    width?: number;
	    height?: number;
	    steps?: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageGenerateRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseURL = source["baseURL"];
	        this.model = source["model"];
	        this.prompt = source["prompt"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.steps = source["steps"];
	    }
	}
	export class ImageGenerateResponse {
	    model?: string;
	    text?: string;
	    images: string[];
	    raw?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageGenerateResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.text = source["text"];
	        this.images = source["images"];
	        this.raw = source["raw"];
	        this.error = source["error"];
	    }
	}
	export class OllamaModel {
	    name: string;
	    modifiedAt?: string;
	    size?: number;
	    family?: string;
	    parameter?: string;
	
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

}

