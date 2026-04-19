interface SpeechRecognitionEvent extends Event {
  readonly resultIndex: number;
  readonly results: SpeechRecognitionResultList;
}

interface SpeechRecognitionErrorEvent extends Event {
  readonly error: string;
  readonly message: string;
}

interface SpeechRecognitionResult {
  readonly isFinal: boolean;
  readonly length: number;
  item(index: number): SpeechRecognitionAlternative;
  [index: number]: SpeechRecognitionAlternative;
}

interface SpeechRecognitionResultList {
  readonly length: number;
  item(index: number): SpeechRecognitionResult;
  [index: number]: SpeechRecognitionResult;
}

interface SpeechRecognitionAlternative {
  readonly transcript: string;
  readonly confidence: number;
}

interface Recognition extends EventTarget {
  continuous: boolean;
  grammars: SpeechGrammarList;
  interimResults: boolean;
  lang: string;
  maxAlternatives: number;
  onaudioend: ((this: Recognition, ev: Event) => any) | null;
  onaudiostart: ((this: Recognition, ev: Event) => any) | null;
  onend: ((this: Recognition, ev: Event) => any) | null;
  onerror: ((this: Recognition, ev: SpeechRecognitionErrorEvent) => any) | null;
  onnomatch: ((this: Recognition, ev: SpeechRecognitionEvent) => any) | null;
  onresult: ((this: Recognition, ev: SpeechRecognitionEvent) => any) | null;
  onsoundend: ((this: Recognition, ev: Event) => any) | null;
  onsoundstart: ((this: Recognition, ev: Event) => any) | null;
  onspeechend: ((this: Recognition, ev: Event) => any) | null;
  onspeechstart: ((this: Recognition, ev: Event) => any) | null;
  onstart: ((this: Recognition, ev: Event) => any) | null;
  start(): void;
  stop(): void;
  abort(): void;
}

interface SpeechGrammarList {
  readonly length: number;
  addFromString(string: string, weight?: number): void;
  addFromURI(src: string, weight?: number): void;
  item(index: number): SpeechGrammar;
  [index: number]: SpeechGrammar;
}

interface SpeechGrammar {
  src: string;
  weight: number;
}

declare global {
  interface Window {
    SpeechRecognition: {
      new (): Recognition;
    };
    webkitSpeechRecognition: {
      new (): Recognition;
    };
  }
}
