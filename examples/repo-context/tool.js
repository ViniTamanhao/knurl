var KNOWN_MANIFESTS = [
  "go.mod",
  "package.json",
  "Cargo.toml",
  "pyproject.toml",
  "mise.toml",
  "Dockerfile",
  "Makefile"
];

var EXT_TO_LANG = {
  ".go": "Go",
  ".js": "JavaScript",
  ".ts": "TypeScript",
  ".py": "Python",
  ".rs": "Rust",
  ".rb": "Ruby",
  ".java": "Java",
  ".sh": "Shell",
  ".md": "Markdown",
  ".json": "JSON",
  ".yaml": "YAML",
  ".yml": "YAML",
  ".toml": "TOML"
};

function detectLanguage(path) {
  var dot = path.lastIndexOf(".");
  if (dot < 0) {
    return "";
  }
  var ext = path.slice(dot).toLowerCase();
  return EXT_TO_LANG[ext] || "";
}

function summarizeFiles(files) {
  var dirSet = {};
  var langCounts = {};
  var i;

  for (i = 0; i < files.length; i++) {
    var p = files[i];
    var slash = p.indexOf("/");
    if (slash > 0) {
      dirSet[p.slice(0, slash)] = true;
    }
    var lang = detectLanguage(p);
    if (lang) {
      langCounts[lang] = (langCounts[lang] || 0) + 1;
    }
  }

  var topDirs = Object.keys(dirSet).sort();
  var languages = Object.keys(langCounts)
    .map(function (name) {
      return { name: name, files: langCounts[name] };
    })
    .sort(function (a, b) {
      if (b.files !== a.files) {
        return b.files - a.files;
      }
      return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
    });

  var manifests = [];
  for (i = 0; i < KNOWN_MANIFESTS.length; i++) {
    var candidate = KNOWN_MANIFESTS[i];
    var st = knurl.fs.stat(candidate);
    if (st.exists && !st.is_dir) {
      manifests.push(candidate);
    }
  }

  return {
    top_directories: topDirs,
    languages: languages,
    manifests: manifests
  };
}

function collectGitSummary(recentLimit, diffBase) {
  var branch;
  try {
    branch = knurl.git.branch();
  } catch (err) {
    if (err && err.code === "NOT_A_GIT_REPO") {
      return null;
    }
    throw err;
  }

  var status = knurl.git.status();
  var commits = [];
  if (recentLimit > 0) {
    var rawLog = knurl.git.log({ limit: recentLimit });
    for (var i = 0; i < rawLog.length; i++) {
      commits.push({
        short_hash: rawLog[i].short_hash,
        author: rawLog[i].author,
        date: rawLog[i].date,
        subject: rawLog[i].subject
      });
    }
  }

  var diffOpts = { stat_only: true };
  if (diffBase) {
    diffOpts.base = diffBase;
  }
  var diff = knurl.git.diff(diffOpts);

  return {
    branch: branch.current,
    detached: branch.detached,
    head: branch.head,
    upstream: branch.upstream || "",
    clean: status.clean,
    staged_count: status.staged.length,
    unstaged_count: status.unstaged.length,
    untracked_count: status.untracked.length,
    recent_commits: commits,
    diff_files: diff.files
  };
}

function main(input) {
  var includeTree = input.include_tree !== false;
  var maxTreeFiles = typeof input.max_tree_files === "number" ? input.max_tree_files : 150;
  var recentCommits = typeof input.recent_commits === "number" ? input.recent_commits : 10;
  var includeReadme = input.include_readme_excerpt !== false;
  var diffBase = typeof input.diff_base === "string" ? input.diff_base : "";

  var files = knurl.fs.glob("**", {
    max_results: 5000,
    include_hidden: false,
    ignore: [".git/**", "node_modules/**", "vendor/**", "bin/**", "dist/**"]
  });

  var summary = summarizeFiles(files);
  var treeFiles = includeTree ? files.slice(0, maxTreeFiles) : [];

  var readmeExcerpt = "";
  if (includeReadme) {
    var readmeStat = knurl.fs.stat("README.md");
    if (readmeStat.exists && !readmeStat.is_dir) {
      readmeExcerpt = knurl.fs.read("README.md", { max_bytes: 2048, truncate: true });
    }
  }

  var gitInfo = collectGitSummary(recentCommits, diffBase);

  return {
    is_git_repo: gitInfo !== null,
    file_count: files.length,
    top_directories: summary.top_directories,
    manifests: summary.manifests,
    languages: summary.languages,
    git: gitInfo,
    tree_files: treeFiles,
    readme_excerpt: readmeExcerpt
  };
}
