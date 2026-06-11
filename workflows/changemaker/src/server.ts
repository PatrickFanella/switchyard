#!/usr/bin/env node
import { loadDotEnvFile, loadServerConfig } from './config.js';
import { startServer } from './server/http.js';

loadDotEnvFile();

const config = loadServerConfig();

if (!config.adminToken) {
  process.stderr.write('changemaker-server: CHANGEMAKER_ADMIN_TOKEN is required\n');
  process.exit(1);
}

const server = await startServer(config);
process.stderr.write(`changemaker-server: listening on http://${config.host}:${config.port}\n`);

process.on('SIGINT', () => {
  server.close();
});

process.on('SIGTERM', () => {
  server.close();
});
