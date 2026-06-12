# Writing Plakar Connectors: a tutorial

The goal of this tutorial is to show step by step how to build a
Plakar connector: a plugin that extends plakar capabilities by
allowing to backup, restore to, or store backups in new places.

It's also the kind of article that I wished my collegues could read
before starting their journey at Plakar and, why not, the whole open
source community as well.

The connector that we're going to build is a novel one: a webdav
connector.  The idea is that it should be able to allow to backup
webdav sources, allow to restore to them, and even store a Kloset over
webdav.
