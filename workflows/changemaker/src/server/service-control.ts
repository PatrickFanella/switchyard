import { spawn } from 'node:child_process';

export type ServiceAction =
  | 'service-status'
  | 'timer-status'
  | 'restart-service'
  | 'restart-timer'
  | 'stop-timer'
  | 'start-timer';

export type ServiceConfirmation = {
  confirm?: boolean;
  confirmationText?: string;
};

export type ServiceExecutionResult = {
  command: string[];
  exitCode: number | null;
  signal: NodeJS.Signals | null;
  stdout: string;
  stderr: string;
};

export type ServiceExecutor = (command: readonly string[]) => Promise<ServiceExecutionResult>;

const COMMANDS: Record<ServiceAction, string[]> = {
  'service-status': ['systemctl', 'status', 'changemaker-daily.service', '--no-pager'],
  'timer-status': ['systemctl', 'status', 'changemaker-daily.timer', '--no-pager'],
  'restart-service': ['sudo', '-n', 'systemctl', 'restart', 'changemaker-daily.service'],
  'restart-timer': ['sudo', '-n', 'systemctl', 'restart', 'changemaker-daily.timer'],
  'stop-timer': ['sudo', '-n', 'systemctl', 'stop', 'changemaker-daily.timer'],
  'start-timer': ['sudo', '-n', 'systemctl', 'start', 'changemaker-daily.timer'],
};

export function createServiceCommand(action: ServiceAction): string[] {
  return [...COMMANDS[action]];
}

export function assertServiceConfirmation(action: ServiceAction, body: ServiceConfirmation): void {
  if (action === 'service-status' || action === 'timer-status') return;

  if (body.confirm !== true || body.confirmationText !== action) {
    throw new Error(`Confirmation text must be ${action}`);
  }
}

export async function executeServiceAction(
  action: ServiceAction,
  executor: ServiceExecutor = runServiceCommand,
): Promise<ServiceExecutionResult> {
  return executor(createServiceCommand(action));
}

async function runServiceCommand(command: readonly string[]): Promise<ServiceExecutionResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(command[0], command.slice(1), {
      shell: false,
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    if (!child.stdout || !child.stderr) {
      reject(new Error('Unable to capture service command output'));
      return;
    }

    let stdout = '';
    let stderr = '';

    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');

    child.stdout.on('data', (chunk: string) => {
      stdout += chunk;
    });

    child.stderr.on('data', (chunk: string) => {
      stderr += chunk;
    });

    child.once('error', (error) => {
      reject(error);
    });

    child.once('close', (exitCode, signal) => {
      resolve({
        command: [...command],
        exitCode,
        signal,
        stdout,
        stderr,
      });
    });
  });
}
