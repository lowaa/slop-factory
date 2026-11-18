// saveFile hands a setup script to the browser's download machinery rather
// than rendering it: even carrying no credential, it is not something to
// invite onto the screen -- an agent name and this server's address are
// still nobody else's business to read over a shoulder.
export function saveFile(filename: string, contents: string) {
  const url = URL.createObjectURL(new Blob([contents], { type: "text/x-shellscript" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
