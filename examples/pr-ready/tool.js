function main(input) {
  var config = JSON.parse(knurl.fs.read(".pr-ready.json"));
  if (config.schema_version !== 1) {
    throw new Error("unsupported .pr-ready.json schema_version; expected 1");
  }
  var branch = knurl.git.branch();
  var status = knurl.git.status();
  var runChecks = input.run_checks === true;
  var exitCode = 0;
  if (runChecks) {
    var result = knurl.exec("project-check", {});
    exitCode = result.exit_code;
    if (exitCode !== 0) {
      knurl.log("project checks failed: " + result.stderr);
    }
  }
  return {
    ready: (status.clean || input.allow_dirty === true) && exitCode === 0,
    branch: branch.current,
    clean: status.clean,
    checks_run: runChecks,
    check_exit_code: exitCode
  };
}
