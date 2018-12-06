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
    commit = "0ddd408d5d60ea76e320503cc7dd091992dee608",
    importpath = "github.com/abbot/go-http-auth",
)

go_repository(
    name = "com_github_urfave_cli",
    commit = "cfb38830724cc34fedffe9a2a29fb54fa9169cd1",
    importpath = "github.com/urfave/cli",
)

go_repository(
    name = "com_github_djherbis_atime",
    commit = "8e47e0e01d08df8b9f840d74299c8ab70a024a30",
    importpath = "github.com/djherbis/atime",
)

go_repository(
    name = "org_golang_x_crypto",
    commit = "ab813273cd59e1333f7ae7bff5d027d4aadf528c",
    importpath = "golang.org/x/crypto",
)

go_repository(
    name = "org_golang_x_net",
    commit = "1e491301e022f8f977054da4c2d852decd59571f",
    importpath = "golang.org/x/net",
)

go_repository(
    name = "com_github_golang_protobuf",
    commit = "b4deda0973fb4c70b50d226b1af49f3da59f5265",
    importpath = "github.com/golang/protobuf",
)

go_repository(
    name = "com_google_cloud_go",
    commit = "0fd7230b2a7505833d5f69b75cbd6c9582401479",
    importpath = "cloud.google.com/go",
)

go_repository(
    name = "in_gopkg_yaml_v2",
    commit = "5420a8b6744d3b0345ab293f6fcba19c978f1183",
    importpath = "gopkg.in/yaml.v2",
)

go_repository(
    name = "org_golang_google_appengine",
    commit = "150dc57a1b433e64154302bdc40b6bb8aefa313a",
    importpath = "google.golang.org/appengine",
)

go_repository(
    name = "org_golang_x_oauth2",
    commit = "ec22f46f877b4505e0117eeaab541714644fdd28",
    importpath = "golang.org/x/oauth2",
)

go_repository(
    name = "com_github_urfave_cli",
    commit = "cfb38830724cc34fedffe9a2a29fb54fa9169cd1",
    importpath = "github.com/urfave/cli",
)

go_repository(
    name = "com_github_google_go_cmp",
    commit = "3af367b6b30c263d47e8895973edcca9a49cf029",
    importpath = "github.com/google/go-cmp",
)
