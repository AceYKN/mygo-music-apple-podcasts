interface HostProcess {
  pid: number;
  write(text: string): void;
  closeStdin(): void;
  readLine(): Promise<string | null>;
  wait(): Promise<number>;
  kill(): void;
  errorOutput(): string;
}
declare const host: {
  dir: string;
  dataDir: string;
  platform: string;
  arch: string;
  language(): string;
  t(text: string): string;
  setting(key: string): string | undefined;
  spawn(command: string, args: string[]): HostProcess;
  signInPrompt(value: { text?: string; qr?: string }): void;
  storage: {
    get(key: string): unknown;
    set(key: string, value: unknown): void;
    remove(key: string): void;
  };
};
