load("@bazel_tools//tools/build_defs/repo:git.bzl", "git_repository")
load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive")

http_archive(
    name = "io_bazel_rules_go",
    url = "https://github.com/bazelbuild/rules_go/releases/download/0.18.6/rules_go-0.18.6.tar.gz",
    sha256 = "f04d2373bcaf8aa09bccb08a98a57e721306c8f6043a2a0ee610fd6853dcde3d",
)

http_archive(
    name = "bazel_gazelle",
    url = "https://github.com/bazelbuild/bazel-gazelle/releases/download/0.17.0/bazel-gazelle-0.17.0.tar.gz",
    sha256 = "3c681998538231a2d24d0c07ed5a7658cb72bfb5fd4bf9911157c0e9ac6a2687",
)

git_repository(
    name = "io_bazel_rules_docker",
    remote = "https://github.com/bazelbuild/rules_docker.git",
    tag = "v0.7.0",
)

load("@io_bazel_rules_go//go:deps.bzl", "go_rules_dependencies", "go_register_toolchains")

go_rules_dependencies()

go_register_toolchains()

load(
    "@io_bazel_rules_docker//go:image.bzl",
    _go_image_repos = "repositories",
)

_go_image_repos()

load("@bazel_gazelle//:deps.bzl", "gazelle_dependencies", "go_repository")

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
    # minio has this dependency
    name = "com_github_go_ini_ini",
    importpath="github.com/go-ini/ini",
    commit="9c8236e659b76e87bf02044d06fde8683008ff3e",
)

go_repository(
    # minio has this dependency
    name = "org_golang_x_net",
    commit = "c39426892332e1bb5ec0a434a079bf82f5d30c54",
    importpath = "golang_org/x/net",
)

go_repository(
    # minio has this dependency
    name = "org_golang_x_sys",
    commit = "d69651ed3497faee15a5363a89578e9991f6d5e2",
    importpath = "golang.org/x/sys",
)

go_repository(
    # minio has this dependency
    name = "com_github_mitchellh_go_homedir",
    commit = "ae18d6b8b3205b561c79e8e5f69bff09736185f4",
    importpath = "github.com/mitchellh/go-homedir",
)

go_repository(
    name = "com_github_minio_go",
    commit = "55c9b2e90ef38c5962d872ebc34b5d7c0e04974c",
    importpath = "github.com/minio/minio-go",
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

# Needed for the googleapis protos used by com_github_bazelbuild_remote_apis
# below.
http_archive(
    name = "googleapis",
    build_file = "BUILD.googleapis",
    sha256 = "7b6ea252f0b8fb5cd722f45feb83e115b689909bbb6a393a873b6cbad4ceae1d",
    strip_prefix = "googleapis-143084a2624b6591ee1f9d23e7f5241856642f4d",
    urls = ["https://github.com/googleapis/googleapis/archive/143084a2624b6591ee1f9d23e7f5241856642f4d.zip"],
)

go_repository(
    name = "com_github_bazelbuild_remote_apis",
    commit = "cd42f25a6c5d7bd97859ab946ddb9a7d8e48b23a",
    importpath = "github.com/bazelbuild/remote-apis",
)

go_repository(
    name = "com_github_google_uuid",
    commit = "c2e93f3ae59f2904160ceaab466009f965df46d6",
    importpath = "github.com/google/uuid",
)

load("@com_github_bazelbuild_remote_apis//:repository_rules.bzl", "switched_rules_by_language")

switched_rules_by_language(
    name = "bazel_remote_apis_imports",
    go = True,
)
