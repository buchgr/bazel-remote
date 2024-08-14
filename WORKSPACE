load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive")

http_archive(
    name = "googleapis",
    sha256 = "9d1a930e767c93c825398b8f8692eca3fe353b9aaadedfbcf1fca2282c85df88",
    strip_prefix = "googleapis-64926d52febbf298cb82a8f472ade4a3969ba922",
    urls = [
        "https://github.com/googleapis/googleapis/archive/64926d52febbf298cb82a8f472ade4a3969ba922.zip",
    ],
)

load("@googleapis//:repository_rules.bzl", "switched_rules_by_language")

switched_rules_by_language(
    name = "com_google_googleapis_imports",
)

http_archive(
    name = "io_bazel_rules_go",
    sha256 = "80a98277ad1311dacd837f9b16db62887702e9f1d1c4c9f796d0121a46c8e184",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/rules_go/releases/download/v0.46.0/rules_go-v0.46.0.zip",
        "https://github.com/bazelbuild/rules_go/releases/download/v0.46.0/rules_go-v0.46.0.zip",
    ],
)

load("@io_bazel_rules_go//go:deps.bzl", "go_register_toolchains", "go_rules_dependencies")

http_archive(
    name = "bazel_gazelle",
    sha256 = "d3fa66a39028e97d76f9e2db8f1b0c11c099e8e01bf363a923074784e451f809",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/bazel-gazelle/releases/download/v0.33.0/bazel-gazelle-v0.33.0.tar.gz",
        "https://github.com/bazelbuild/bazel-gazelle/releases/download/v0.33.0/bazel-gazelle-v0.33.0.tar.gz",
    ],
)

load("@bazel_gazelle//:deps.bzl", "gazelle_dependencies", "go_repository")

go_rules_dependencies()

go_register_toolchains(version = "1.23.0")

http_archive(
    name = "rules_proto",
    sha256 = "dc3fb206a2cb3441b485eb1e423165b231235a1ea9b031b4433cf7bc1fa460dd",
    strip_prefix = "rules_proto-5.3.0-21.7",
    urls = [
        "https://github.com/bazelbuild/rules_proto/archive/refs/tags/5.3.0-21.7.tar.gz",
    ],
)

load("@rules_proto//proto:repositories.bzl", "rules_proto_dependencies", "rules_proto_toolchains")

rules_proto_dependencies()

rules_proto_toolchains()

load("//:deps.bzl", "go_dependencies")

# gazelle:repository_macro deps.bzl%go_dependencies
go_dependencies()

go_repository(
    name = "com_github_aws_aws_sdk_go",
    importpath = "github.com/aws/aws-sdk-go",
    sum = "h1:O8VH+bJqgLDguqkH/xQBFz5o/YheeZqgcOYIgsTVWY4=",
    version = "v1.44.256",
)

go_repository(
    name = "com_github_jmespath_go_jmespath",
    importpath = "github.com/jmespath/go-jmespath",
    sum = "h1:BEgLn5cpjn8UN1mAw4NjwDrS35OdebyEtFe+9YPoQUg=",
    version = "v0.4.0",
)

go_repository(
    name = "com_github_jmespath_go_jmespath_internal_testify",
    importpath = "github.com/jmespath/go-jmespath/internal/testify",
    sum = "h1:shLQSRRSCCPj3f2gpwzGwWFoC7ycTf1rcQZHOlsJ6N8=",
    version = "v1.5.1",
)

go_repository(
    name = "com_github_johannesboyne_gofakes3",
    importpath = "github.com/johannesboyne/gofakes3",
    sum = "h1:O7syWuYGzre3s73s+NkgB8e0ZvsIVhT/zxNU7V1gHK8=",
    version = "v0.0.0-20230506070712-04da935ef877",
)

go_repository(
    name = "com_github_ryszard_goskiplist",
    importpath = "github.com/ryszard/goskiplist",
    sum = "h1:GHRpF1pTW19a8tTFrMLUcfWwyC0pnifVo2ClaLq+hP8=",
    version = "v0.0.0-20150312221310-2dfbae5fcf46",
)

go_repository(
    name = "com_github_shabbyrobe_gocovmerge",
    importpath = "github.com/shabbyrobe/gocovmerge",
    sum = "h1:WnNuhiq+FOY3jNj6JXFT+eLN3CQ/oPIsDPRanvwsmbI=",
    version = "v0.0.0-20190829150210-3e036491d500",
)

go_repository(
    name = "com_github_spf13_afero",
    importpath = "github.com/spf13/afero",
    sum = "h1:qgMbHoJbPbw579P+1zVY+6n4nIFuIchaIjzZ/I/Yq8M=",
    version = "v1.2.1",
)

go_repository(
    name = "in_gopkg_mgo_v2",
    importpath = "gopkg.in/mgo.v2",
    sum = "h1:xcEWjVhvbDy+nHP67nPDDpbYrY+ILlfndk4bRioVHaU=",
    version = "v2.0.0-20180705113604-9856a29383ce",
)

go_repository(
    name = "io_etcd_go_bbolt",
    importpath = "go.etcd.io/bbolt",
    sum = "h1:XAzx9gjCb0Rxj7EoqcClPD1d5ZBxZJk0jbuoPHenBt0=",
    version = "v1.3.5",
)

gazelle_dependencies()

http_archive(
    name = "rules_pkg",
    sha256 = "8f9ee2dc10c1ae514ee599a8b42ed99fa262b757058f65ad3c384289ff70c4b8",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/rules_pkg/releases/download/0.9.1/rules_pkg-0.9.1.tar.gz",
        "https://github.com/bazelbuild/rules_pkg/releases/download/0.9.1/rules_pkg-0.9.1.tar.gz",
    ],
)

load("@rules_pkg//:deps.bzl", "rules_pkg_dependencies")

rules_pkg_dependencies()

# Use a new-enough aspect-build/bazel-lib to avoid "Error validating output TreeArtifact
# File when building an oci_image" errors.
# See https://github.com/bazel-contrib/rules_oci/issues/425#issuecomment-1860211483
http_archive(
    name = "aspect_bazel_lib",
    sha256 = "fc8bd670380eaba5314769abbe9fee21d641e3da06d9d26b8073a301f6d62332",
    strip_prefix = "bazel-lib-2.1.0",
    url = "https://github.com/aspect-build/bazel-lib/releases/download/v2.1.0/bazel-lib-v2.1.0.tar.gz",
)

http_archive(
    name = "rules_oci",
    sha256 = "686f871f9697e08877b85ea6c16c8d48f911bf466c3aeaf108ca0ab2603c7306",
    strip_prefix = "rules_oci-1.5.1",
    url = "https://github.com/bazel-contrib/rules_oci/releases/download/v1.5.1/rules_oci-v1.5.1.tar.gz",
)

load("@rules_oci//oci:dependencies.bzl", "rules_oci_dependencies")

rules_oci_dependencies()

load("@rules_oci//oci:repositories.bzl", "LATEST_CRANE_VERSION", "oci_register_toolchains")

oci_register_toolchains(
    name = "oci",
    crane_version = LATEST_CRANE_VERSION,
    # Uncommenting the zot toolchain will cause it to be used instead of crane for some tasks.
    # Note that it does not support docker-format images.
    # zot_version = LATEST_ZOT_VERSION,
)

load("@rules_oci//oci:pull.bzl", "oci_pull")

oci_pull(
    name = "cgo_amd64_base",
    registry = "gcr.io",
    repository = "distroless/base-nossl-debian12",
    # See https://github.com/buchgr/bazel-remote/issues/605 and https://github.com/GoogleContainerTools/distroless/issues/1098
    # TODO: specify this by digest instead? Where can I find that?
    tag = "nonroot-amd64",
)

oci_pull(
    name = "cgo_arm64_base",
    registry = "gcr.io",
    repository = "distroless/base-nossl-debian12",
    tag = "nonroot-arm64",
)
