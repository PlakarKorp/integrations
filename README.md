# Plakar integrations

This repository holds all the official Plakar integrations.

## Contributing

PRs are welcome!  Each integration has its own branch, so for e.g. if
you want to submit a change for the s3 integration:

	$ git clone -b integration/s3 ssh://git@github.com/PlakarKorp/integrations.git

Don't forget to pick the right base branch when opening a PR!  (in
this case, `integration/s3`.)


## Advanced: working on multiple integrations

If you need to work concurrently on multiple integrations, `git
switch` all the time can become annoying.  Worktrees could be an
answer though.

	$ git clone ssh://git@github.com/PlakarKorp/integrations.git
	$ cd integrations
	$ git worktree add ../s3  integration/s3
	$ git worktree add ../k8s integration/k8s

Then, you should be able to find the s3 code in `../s3/s3`, and
similarly the kubernetes one in `../k8s/k8s`.  Unfortunately git does
not allow to strip components off the front of the path, so the
"double integration name" is not remove-able.

If using [got](https://gameoftrees.org) instead:

	$ cd /git
	$ got clone ssh://git@github.com/PlakarKorp/integrations.git
	$ cd ~/w/pk  # workspace for plakar korp
	$ got checkout -b integration/s3  -p s3  /git/integrations.git
	$ got checkout -b integration/k8s -p k8s /git/integrations.git

This should create two checkouts by stripping the leading directory.


## Importing a new integration

Requirements:
1. each integration should live in its own branch which has only the
   history for that integration;
2. each integration should be in a sub-directory, because we merge
   stable releases in main.

The only exception to rule 2 is the `.github` directory that *must*
live at the top-level directory.

Keeping this in mind, to add a new integration "foo":

	$ git worktree add --orphan -b integration/foo ../foo
	$ mkdir ../foo/foo
	$ cd ../foo/foo

make sure there's a "double name" in the path, i.e.

	$ pwd
	.../foo/foo

Because this is important for the requirement 2!  Remember: Only the
`.github` directory can, in case it is used, be at the top-level of your
worktree.

Then, actually write the code for the integration:

	$ go mod init github.com/PlakarKorp/integrations/foo
	$ ed connectors.go
	... write the code
	$ ed manifest.yaml
	... write the manifest
	$ cp ../../integrations/example/Makefile .
	$ ed Makefile
	... edit accordingly to the foo integration
	... usually ,s/example/foo/g should be enough

At this point, you should have a working integration that you can test with:

	$ make package install

At this point, feel free to commit in the `integration/foo` branch.

If using [got](https://gameoftrees.org) the process is similar:

	$ mkdir -p foo/foo
	$ cd foo/foo
	$ go mod init ...
	$ ed connector.go # happy hacking, etc...
	$ cp ../integrations/example/Makefile .
	$ sed -i s/example/foo/g Makefile

Then, import it as a standalone branch:

	$ cd /git/integrations.git
	$ got import -b integration/foo -m 'inital import of foo' /path/to/foo # NOT foo/foo !!

Then, check out a fresh work tree as usual with the -p foo option:

	$ got checkout -b integration/foo -p foo /git/integrations.git 

## Releasing an integration

When you're ready to finally release an integration, be it for the
first time or as an update, the process is as follows.

Let's assume you'd like to release the "foo" integration v1.1.0:

	$ cd path/to/foo/foo # dobule foo!
	$ ed Makefile
	... fix VERSION if needed
	$ git commit -m 'bump version' Makefile # in case it was edited
	$ make clean package install
	$ plakar do stuff # test it one more time! ;-)
	$ git tag -a foo/v1.1.0
	... fill the release note, usually they are in the form:
	foo v1.1.0

	* fixed foobaring of foos
	* introduced the ability to bar'ing the baaz
	* etc...
	$ git push
	$ git push --tags

Then, merge the stable release in main:

	$ cd ../../integrations
	$ git merge foo/v1.1.0
	$ git push

If using [got](https://gameoftrees.org):

	$ cd path/to/foo
	$ ed Makefile
	... fix VERSION if needed
	$ got commit -m 'bump version' Makefile # in case it was edited
	$ make clean package install
	$ plakar do stuff # test it one more time! ;-)
	$ got tag foo/v1.1.0
	... fill the release note, usually they are in the form:
	foo v1.1.0

	* fixed foobaring of foos
	* introduced the ability to bar'ing the baaz
	* etc...
	$ got send -t v1.1.0

In order to perform merges to the main branch separate a separate work tree
is needed which contains the entire repository tree:

	$ cd ~/w/pk  # workspace for plakar korp
	$ got checkout -b main /git/integrations.git

Then, merge the integration's release tag into the main branch:

	$ cd integrations
	$ got merge refs/tags/foo/v1.1.0 # got versions < 0.127 do not support merging of tags, try using the branch name integration/foo instead
	$ got send

Finally, don't forget to update
[hub.git](https://github.com/PlakarKorp/hub.git) and bump the version
there as well.
