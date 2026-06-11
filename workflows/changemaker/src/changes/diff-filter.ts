export type FilteredDiff = {
  text: string;
  files: string[];
  truncated: boolean;
};

const excludedPathPatterns = [
  /(^|\/)dist\//,
  /(^|\/)build\//,
  /(^|\/)vendor\//,
  /(^|\/)node_modules\//,
  /(^|\/)pnpm-lock\.yaml$/,
  /(^|\/)package-lock\.json$/,
  /(^|\/)yarn\.lock$/,
];

export function filterDiff(rawDiff: string, maxChars = 12_000): FilteredDiff {
  const sections = splitDiffSections(rawDiff);
  const files: string[] = [];
  const kept: string[] = [];
  let truncated = false;

  for (const section of sections) {
    const filePath = diffSectionPath(section);
    if (filePath) {
      files.push(filePath);
    }
    if (!filePath || shouldExcludePath(filePath) || isBinarySection(section)) {
      continue;
    }
    kept.push(section);
  }

  let text = kept.join('\n');
  if (text.length > maxChars) {
    text = `${text.slice(0, maxChars)}\n\n[diff truncated]`;
    truncated = true;
  }

  return {
    text,
    files: [...new Set(files)],
    truncated,
  };
}

function splitDiffSections(rawDiff: string): string[] {
  return rawDiff
    .split('\ndiff --git ')
    .map((section, index) => (index === 0 ? section : `diff --git ${section}`))
    .filter((section) => section.trim().length > 0);
}

function diffSectionPath(section: string): string | undefined {
  const firstLine = section.split('\n')[0] ?? '';
  const match = /^diff --git a\/(.+?) b\/(.+)$/.exec(firstLine);
  return match?.[2];
}

function shouldExcludePath(path: string): boolean {
  return excludedPathPatterns.some((pattern) => pattern.test(path));
}

function isBinarySection(section: string): boolean {
  return section.includes('Binary files ') || section.includes('GIT binary patch');
}
