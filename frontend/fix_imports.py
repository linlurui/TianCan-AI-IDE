#!/usr/bin/env python3
import os
import re

for root, dirs, files in os.walk('bindings'):
    for f in files:
        if f.endswith('.ts'):
            path = os.path.join(root, f)
            with open(path, 'r') as file:
                content = file.read()
            # Fix runtime imports
            content = content.replace('from "/wails/runtime.js"', 'from "../../wailsjs/runtime.js"')
            # Fix time imports
            content = re.sub(r'from "[^"]*time/models\.js"', 'from "../../time/models.js"', content)
            with open(path, 'w') as file:
                file.write(content)
print("Done fixing imports")
