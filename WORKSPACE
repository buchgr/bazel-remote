load("@bazel_tools//tools/build_defs/repo:git.bzl", "git_repository")
load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive")

http_archive(
    name = "io_bazel_rules_go",
    url = "https://github.com/bazelbuild/rules_go/releases/download/0.15.8/rules_go-0.15.8.tar.gz",
    sha256 = "ca79fed5b24dcc0696e1651ecdd916f7a11111283ba46ea07633a53d8e1f5199",
)

http_archive(
    name = "bazel_gazelle",
    url = "https://github.com/bazelbuild/bazel-gazelle/releases/download/0.15.0/bazel-gazelle-0.15.0.tar.gz",
    sha256 = "6e875ab4b6bf64a38c352887760f21203ab054676d9c1b274963907e0768740d",
)

git_repository(
    name = "io_bazel_rules_docker",
    remote = "https://github.com/bazelbuild/rules_docker.git",
    tag = "v0.5.1",
)

load(
    "@io_bazel_rules_docker//go:image.bzl",
    _go_image_repos = "repositories",
)

_go_image_repos()

load("@io_bazel_rules_go//go:def.bzl", "go_register_toolchains", "go_rules_dependencies")

go_rules_dependencies()

go_register_toolchains()

load("@bazel_gazelle//:deps.bzl", "gazelle_dependencies")
load("@bazel_gazelle//:def.bzl", "go_repository")

gazelle_dependencies()

go_repository(
    name = "com_github_abbot_go_http_auth",
    importpath = "github.com/abbot/go-http-auth",
    tag = "v0.4.0",
)

go_repository(
    name = "com_github_urfave_cli",
    commit = "cfb38830724cc34fedffe9a2a29fb54fa9169cd1",
    importpath = "github.com/urfave/cli",
)

go_repository(
    name = "com_github_djherbis_atime",
    importpath = "github.com/djherbis/atime",
    tag = "v1.0.0",
)

go_repository(
    name = "org_golang_x_crypto",
    commit = "ab813273cd59",
    importpath = "golang.org/x/crypto",
)

go_repository(
    name = "org_golang_x_net",
    commit = "1e491301e022",
    importpath = "golang.org/x/net",
)

go_repository(
    name = "com_github_golang_protobuf",
    importpath = "github.com/golang/protobuf",
    tag = "v1.1.0",
)

go_repository(
    name = "com_google_cloud_go",
    importpath = "cloud.google.com/go",
    tag = "v0.23.0",
)

go_repository(
    name = "in_gopkg_yaml_v2",
    importpath = "gopkg.in/yaml.v2",
    tag = "v2.2.1",
)

go_repository(
    name = "org_golang_google_appengine",
    importpath = "google.golang.org/appengine",
    tag = "v1.0.0",
)

go_repository(
    name = "org_golang_x_oauth2",
    commit = "ec22f46f877b",
    importpath = "golang.org/x/oauth2",
)

go_repository(
    name = "com_github_urfave_cli",
    commit = "cfb38830724cc34fedffe9a2a29fb54fa9169cd1",
    importpath = "github.com/urfave/cli",
)

go_repository(
    name = "com_github_google_go_cmp",
    importpath = "github.com/google/go-cmp",
    tag = "v0.2.0",
)

go_repository(
    name = "com_github_nightlyone_lockfile",
    commit = "0ad87eef1443",
    importpath = "github.com/nightlyone/lockfile",
)

go_repository(
    name = "in_gopkg_check_v1",
    commit = "20d25e280405",
    importpath = "gopkg.in/check.v1",
)
