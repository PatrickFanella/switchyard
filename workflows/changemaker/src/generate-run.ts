import { resolveGenerateWindow } from './config.js';

export type GeneratePlanInput = {
  config: {
    now: Date;
    since: Date;
    lookbackHours: number;
    cadenceHours: number;
    nowWindow: boolean;
    manualWindow: boolean;
  };
  lastPostAt?: Date;
};

export type GeneratePlan = {
  since: Date;
  until: Date;
};

export function createGeneratePlan(input: GeneratePlanInput): GeneratePlan {
  if (input.config.manualWindow) {
    return { since: input.config.since, until: input.config.now };
  }

  return resolveGenerateWindow({
    now: input.config.now,
    lookbackHours: input.config.lookbackHours,
    cadenceHours: input.config.cadenceHours,
    lastPostAt: input.lastPostAt,
    nowWindow: input.config.nowWindow,
  });
}
