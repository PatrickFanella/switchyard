export type BackfillCadence = 'daily' | 'weekly' | 'monthly' | 'quarterly';

export type BackfillWindow = {
  from: string;
  to: string;
  cadence: BackfillCadence;
  timeTravelStrict: boolean;
};

export function createBackfillWindows(input: {
  start: string;
  end: string;
  cadence: BackfillCadence;
  timeTravelStrict: boolean;
}): BackfillWindow[] {
  const start = parseDate(input.start, 'start');
  const end = parseDate(input.end, 'end');
  if (start >= end) {
    throw new Error('--start must be before --end');
  }

  const stepMs = cadenceDays(input.cadence) * 24 * 60 * 60 * 1000;
  const windows: BackfillWindow[] = [];
  for (let cursor = start; cursor < end; cursor = new Date(cursor.getTime() + stepMs)) {
    const next = new Date(Math.min(cursor.getTime() + stepMs, end.getTime()));
    windows.push({
      from: cursor.toISOString(),
      to: next.toISOString(),
      cadence: input.cadence,
      timeTravelStrict: input.timeTravelStrict,
    });
  }
  return windows;
}

function cadenceDays(cadence: BackfillCadence): number {
  if (cadence === 'daily') return 1;
  if (cadence === 'weekly') return 7;
  if (cadence === 'monthly') return 31;
  return 92;
}

function parseDate(value: string, label: string): Date {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    throw new Error(`Invalid ${label} date: ${value}`);
  }
  return date;
}
