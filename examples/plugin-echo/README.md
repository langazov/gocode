# plugin-echo

A worked example of a gocode **process plugin**: a separate executable the host
spawns and speaks newline-delimited JSON-RPC 2.0 to over stdio.

It declares one tool (`echo`) and two hooks
(`tool.execute.after`, `experimental.chat.system.transform`).

## Try it

One command installs it under `~/.config/gocode/plugin/` **and** enables it in
your global config, so plain `gocode` loads it in any directory:

```sh
make install-example-plugin
```

Copying alone would not be enough — a plugin runs only when the config's
`plugin` array names it. Use `CONFIGURE=0` to skip the config edit, and
`make uninstall-plugin NAME=plugin-echo` to undo both halves.

Or build it in place and point at the directory:

```sh
make example-plugin
```

```json
{ "plugin": [["./examples/plugin-echo", { "banner": "hi" }]] }
```

The in-place build has to land *next to* `gocode-plugin.json`, because that
manifest names `./plugin-echo` relative to the plugin's own directory — which
is what lets a plugin ship its own executable.

Either way, `gocode debug info` lists what resolved, and `gocode` will now
advertise an `echo` tool. `make uninstall-plugin NAME=plugin-echo` removes it.

## The protocol in three rules

1. Answer `initialize` with a manifest naming the hooks and tools you
   implement. The host only calls what you declare.
2. Answer `hook` by returning the output you were handed, **mutated**. You
   receive the current output, not a blank one — earlier plugins may already
   have changed it.
3. Exit on `shutdown` or on EOF.

Never write anything but protocol messages to stdout. Diagnostics go to stderr,
which the host captures into its log.

Nothing here is Go-specific. The same program in Python or Node works
identically — a plugin is an executable, not a library.

See [documentation/09-integrations.md](../../documentation/09-integrations.md)
for the hook catalog and the two plugin tiers.
