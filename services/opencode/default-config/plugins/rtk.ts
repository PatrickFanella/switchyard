export default {
  id: 'switchyard.rtk',
  setup: async (ctx) => {
    await ctx.shell.hook('create.before', (event) => {
      if (!event.command.trimStart().startsWith('rtk ')) {
        event.command = `rtk ${event.command}`;
      }
    });
  },
};
