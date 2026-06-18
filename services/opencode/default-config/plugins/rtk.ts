export default async function rtkPlugin() {
  return {
    'tool.execute.before': async (input, output) => {
      if (input.tool !== 'bash') return;
      const command = output.args?.command;
      if (typeof command !== 'string') return;
      if (command.trimStart().startsWith('rtk ')) return;
      output.args.command = `rtk ${command}`;
    },
  };
}
