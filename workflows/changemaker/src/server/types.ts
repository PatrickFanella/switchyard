export type RedactedSecret = {
  present: boolean;
  redacted: '••••••';
};

export type PublicServerConfig = {
  host: string;
  port: number;
  postsDir: string;
  dataDir: string;
  serviceControlEnabled: boolean;
};

export type ApiError = {
  error: string;
};
