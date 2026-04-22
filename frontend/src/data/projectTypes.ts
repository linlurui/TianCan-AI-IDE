export interface ProjectCategory {
  id: string;
  emoji: string;
  label: string;
  description: string;
  color: string;
  types: ProjectTypeItem[];
}

export interface TemplateFile {
  name: string;
  content: string;
}

export interface ProjectTypeItem {
  id: string;
  label: string;
  description: string;
  extensions: string[];
  setupType?: string;
  templateFiles?: TemplateFile[];
}

export const PROJECT_CATEGORIES: ProjectCategory[] = [
  {
    id: "web",
    emoji: "🌐",
    label: "Web 前端",
    description: "HTML/CSS/JS、Vue、React 等前端项目",
    color: "#4fc3f7",
    types: [
      {
        id: "web-html",
        label: "HTML / CSS / JS",
        description: "纯静态网页，零框架依赖",
        extensions: ["esbenp.prettier-vscode"],
        templateFiles: [
          { name: "index.html", content: `<!DOCTYPE html>\n<html lang="zh">\n<head>\n  <meta charset="UTF-8">\n  <meta name="viewport" content="width=device-width, initial-scale=1.0">\n  <title>My Project</title>\n  <link rel="stylesheet" href="style.css">\n</head>\n<body>\n  <h1>Hello World</h1>\n  <script src="main.js"></script>\n</body>\n</html>` },
          { name: "style.css", content: `* { box-sizing: border-box; margin: 0; padding: 0; }\nbody { font-family: sans-serif; line-height: 1.5; }` },
          { name: "main.js", content: `console.log('Hello World');` },
        ],
      },
      {
        id: "web-vue",
        label: "Vue 3",
        description: "Vue 3 + Vite，Composition API",
        extensions: ["vue.volar", "esbenp.prettier-vscode"],
        setupType: "npm",
        templateFiles: [
          { name: "package.json", content: `{\n  "name": "my-vue-app",\n  "version": "0.0.1",\n  "private": true,\n  "scripts": {\n    "dev": "vite",\n    "build": "vite build",\n    "preview": "vite preview"\n  },\n  "dependencies": {\n    "vue": "^3.4.0"\n  },\n  "devDependencies": {\n    "@vitejs/plugin-vue": "^5.0.0",\n    "vite": "^5.0.0"\n  }\n}` },
          { name: "vite.config.js", content: `import { defineConfig } from 'vite'\nimport vue from '@vitejs/plugin-vue'\nexport default defineConfig({ plugins: [vue()] })` },
          { name: "src/App.vue", content: `<template>\n  <div>\n    <h1>Hello Vue 3!</h1>\n  </div>\n</template>\n\n<script setup>\n</script>` },
          { name: "src/main.js", content: `import { createApp } from 'vue'\nimport App from './App.vue'\ncreateApp(App).mount('#app')` },
          { name: "index.html", content: `<!DOCTYPE html>\n<html lang="zh">\n<head>\n  <meta charset="UTF-8">\n  <meta name="viewport" content="width=device-width, initial-scale=1.0">\n  <title>Vue App</title>\n</head>\n<body>\n  <div id="app"></div>\n  <script type="module" src="/src/main.js"></script>\n</body>\n</html>` },
        ],
      },
      {
        id: "web-react",
        label: "React 18",
        description: "React 18 + Vite，JSX/TSX",
        extensions: ["esbenp.prettier-vscode"],
        setupType: "npm",
        templateFiles: [
          { name: "package.json", content: `{\n  "name": "my-react-app",\n  "version": "0.0.1",\n  "private": true,\n  "scripts": {\n    "dev": "vite",\n    "build": "vite build"\n  },\n  "dependencies": {\n    "react": "^18.0.0",\n    "react-dom": "^18.0.0"\n  },\n  "devDependencies": {\n    "@vitejs/plugin-react": "^4.0.0",\n    "vite": "^5.0.0"\n  }\n}` },
          { name: "vite.config.js", content: `import { defineConfig } from 'vite'\nimport react from '@vitejs/plugin-react'\nexport default defineConfig({ plugins: [react()] })` },
          { name: "src/App.jsx", content: `export default function App() {\n  return <h1>Hello React!</h1>;\n}` },
          { name: "src/main.jsx", content: `import React from 'react'\nimport ReactDOM from 'react-dom/client'\nimport App from './App'\nReactDOM.createRoot(document.getElementById('root')).render(<App />)` },
          { name: "index.html", content: `<!DOCTYPE html>\n<html lang="zh">\n<head>\n  <meta charset="UTF-8">\n  <meta name="viewport" content="width=device-width, initial-scale=1.0">\n  <title>React App</title>\n</head>\n<body>\n  <div id="root"></div>\n  <script type="module" src="/src/main.jsx"></script>\n</body>\n</html>` },
        ],
      },
      {
        id: "web-ts",
        label: "TypeScript",
        description: "TypeScript 通用项目",
        extensions: ["esbenp.prettier-vscode"],
        templateFiles: [
          { name: "tsconfig.json", content: `{\n  "compilerOptions": {\n    "target": "ES2020",\n    "module": "ESNext",\n    "moduleResolution": "bundler",\n    "strict": true,\n    "outDir": "dist"\n  },\n  "include": ["src"]\n}` },
          { name: "src/index.ts", content: `function greet(name: string): string {\n  return \`Hello, \${name}!\`;\n}\n\nconsole.log(greet('TypeScript'));` },
        ],
      },
    ],
  },
  {
    id: "mobile",
    emoji: "📱",
    label: "移动端",
    description: "UniApp、Flutter 跨平台移动应用",
    color: "#81c784",
    types: [
      {
        id: "uniapp",
        label: "UniApp",
        description: "Vue 语法，编译到小程序 / App / H5",
        extensions: ["vue.volar"],
        templateFiles: [
          { name: "pages/index/index.vue", content: `<template>\n  <view class="container">\n    <text>Hello UniApp!</text>\n  </view>\n</template>\n\n<script setup>\n</script>\n\n<style>\n.container { padding: 20px; }\n</style>` },
          { name: "manifest.json", content: `{\n  "name": "my-uniapp",\n  "appid": "",\n  "description": "",\n  "versionName": "1.0.0",\n  "versionCode": "100"\n}` },
          { name: "pages.json", content: `{\n  "pages": [\n    {\n      "path": "pages/index/index",\n      "style": { "navigationBarTitleText": "首页" }\n    }\n  ]\n}` },
        ],
      },
      {
        id: "flutter",
        label: "Flutter",
        description: "Dart 跨平台 UI 框架",
        extensions: ["dart-code.flutter", "dart-code.dart-code"],
        templateFiles: [
          { name: "lib/main.dart", content: `import 'package:flutter/material.dart';\n\nvoid main() => runApp(const MyApp());\n\nclass MyApp extends StatelessWidget {\n  const MyApp({super.key});\n  @override\n  Widget build(BuildContext context) {\n    return const MaterialApp(\n      home: Scaffold(\n        body: Center(child: Text('Hello Flutter!')),\n      ),\n    );\n  }\n}` },
          { name: "pubspec.yaml", content: `name: my_flutter_app\ndescription: A new Flutter project.\nversion: 1.0.0+1\nenvironment:\n  sdk: '>=3.0.0 <4.0.0'\ndependencies:\n  flutter:\n    sdk: flutter\nflutter:\n  uses-material-design: true` },
        ],
      },
      {
        id: "react-native",
        label: "React Native",
        description: "React 语法跨平台 App",
        extensions: ["esbenp.prettier-vscode"],
        templateFiles: [
          { name: "App.tsx", content: `import React from 'react';\nimport { View, Text, StyleSheet } from 'react-native';\n\nexport default function App() {\n  return (\n    <View style={styles.container}>\n      <Text>Hello React Native!</Text>\n    </View>\n  );\n}\n\nconst styles = StyleSheet.create({\n  container: { flex: 1, alignItems: 'center', justifyContent: 'center' },\n});` },
        ],
      },
    ],
  },
  {
    id: "java",
    emoji: "☕",
    label: "Java / Kotlin",
    description: "Spring Boot、Maven、Gradle JVM 项目",
    color: "#ffb74d",
    types: [
      {
        id: "java-maven",
        label: "Java + Maven",
        description: "标准 Maven 项目结构",
        extensions: ["redhat.java"],
        setupType: "maven",
        templateFiles: [
          { name: "pom.xml", content: `<?xml version="1.0" encoding="UTF-8"?>\n<project xmlns="http://maven.apache.org/POM/4.0.0"\n         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"\n         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 http://maven.apache.org/xsd/maven-4.0.0.xsd">\n  <modelVersion>4.0.0</modelVersion>\n  <groupId>com.example</groupId>\n  <artifactId>my-app</artifactId>\n  <version>1.0-SNAPSHOT</version>\n  <properties>\n    <maven.compiler.source>17</maven.compiler.source>\n    <maven.compiler.target>17</maven.compiler.target>\n    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>\n  </properties>\n</project>` },
          { name: "src/main/java/com/example/Main.java", content: `package com.example;\n\npublic class Main {\n    public static void main(String[] args) {\n        System.out.println("Hello, Java!");\n    }\n}` },
        ],
      },
      {
        id: "java-gradle",
        label: "Java + Gradle",
        description: "Gradle 构建 Java 项目",
        extensions: ["redhat.java"],
        setupType: "gradle",
        templateFiles: [
          { name: "build.gradle", content: `plugins {\n    id 'java'\n    id 'application'\n}\n\ngroup = 'com.example'\nversion = '1.0-SNAPSHOT'\n\njava {\n    sourceCompatibility = JavaVersion.VERSION_17\n}\n\napplication {\n    mainClass = 'com.example.Main'\n}` },
          { name: "settings.gradle", content: `rootProject.name = 'my-app'` },
          { name: "src/main/java/com/example/Main.java", content: `package com.example;\n\npublic class Main {\n    public static void main(String[] args) {\n        System.out.println("Hello, Java!");\n    }\n}` },
        ],
      },
      {
        id: "kotlin-gradle",
        label: "Kotlin + Gradle",
        description: "Kotlin JVM 项目",
        extensions: ["redhat.java", "fwcd.kotlin"],
        setupType: "gradle",
        templateFiles: [
          { name: "build.gradle.kts", content: `plugins {\n    kotlin("jvm") version "1.9.0"\n    application\n}\n\ngroup = "com.example"\nversion = "1.0-SNAPSHOT"\n\napplication {\n    mainClass.set("com.example.MainKt")\n}` },
          { name: "settings.gradle.kts", content: `rootProject.name = "my-kotlin-app"` },
          { name: "src/main/kotlin/com/example/Main.kt", content: `package com.example\n\nfun main() {\n    println("Hello, Kotlin!")\n}` },
        ],
      },
    ],
  },
  {
    id: "python",
    emoji: "🐍",
    label: "Python",
    description: "通用 Python、FastAPI、Django 等",
    color: "#f48fb1",
    types: [
      {
        id: "python-general",
        label: "Python 通用",
        description: "标准 Python 脚本项目",
        extensions: ["ms-python.python"],
        templateFiles: [
          { name: "main.py", content: `def main():\n    print("Hello, Python!")\n\nif __name__ == "__main__":\n    main()` },
        ],
      },
      {
        id: "python-fastapi",
        label: "FastAPI",
        description: "现代 Python Web API 框架",
        extensions: ["ms-python.python"],
        setupType: "pip",
        templateFiles: [
          { name: "main.py", content: `from fastapi import FastAPI\n\napp = FastAPI()\n\n@app.get("/")\ndef root():\n    return {"message": "Hello World"}` },
          { name: "requirements.txt", content: `fastapi\nuvicorn[standard]` },
        ],
      },
      {
        id: "python-django",
        label: "Django",
        description: "全功能 Python Web 框架",
        extensions: ["ms-python.python"],
        setupType: "pip",
        templateFiles: [
          { name: "requirements.txt", content: `django>=4.2\n` },
          { name: "manage.py", content: `#!/usr/bin/env python\nimport os\nimport sys\nif __name__ == '__main__':\n    os.environ.setdefault('DJANGO_SETTINGS_MODULE', 'config.settings')\n    from django.core.management import execute_from_command_line\n    execute_from_command_line(sys.argv)` },
        ],
      },
    ],
  },
  {
    id: "go",
    emoji: "🐹",
    label: "Go",
    description: "Go 语言服务端 / CLI 项目",
    color: "#80deea",
    types: [
      {
        id: "go-module",
        label: "Go 模块",
        description: "标准 go.mod 项目",
        extensions: ["golang.go"],
        setupType: "go",
        templateFiles: [
          { name: "main.go", content: `package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("Hello, Go!")\n}` },
        ],
      },
      {
        id: "go-gin",
        label: "Go + Gin Web",
        description: "Gin HTTP 框架 Web 服务",
        extensions: ["golang.go"],
        setupType: "go",
        templateFiles: [
          { name: "main.go", content: `package main\n\nimport "github.com/gin-gonic/gin"\n\nfunc main() {\n\tr := gin.Default()\n\tr.GET("/", func(c *gin.Context) {\n\t\tc.JSON(200, gin.H{"message": "Hello, Gin!"})\n\t})\n\tr.Run(":8080")\n}` },
          { name: "go.mod", content: `module example.com/myapp\n\ngo 1.21\n\nrequire github.com/gin-gonic/gin v1.9.0` },
        ],
      },
    ],
  },
  {
    id: "rust",
    emoji: "🦀",
    label: "Rust",
    description: "系统编程 Cargo 项目",
    color: "#ef9a9a",
    types: [
      {
        id: "rust-binary",
        label: "Rust Binary",
        description: "可执行二进制 Cargo 项目",
        extensions: ["rust-lang.rust-analyzer"],
        setupType: "cargo",
        templateFiles: [
          { name: "Cargo.toml", content: `[package]\nname = "my-project"\nversion = "0.1.0"\nedition = "2021"\n` },
          { name: "src/main.rs", content: `fn main() {\n    println!("Hello, Rust!");\n}` },
        ],
      },
      {
        id: "rust-lib",
        label: "Rust Library",
        description: "Rust 库 crate",
        extensions: ["rust-lang.rust-analyzer"],
        setupType: "cargo",
        templateFiles: [
          { name: "Cargo.toml", content: `[package]\nname = "my-lib"\nversion = "0.1.0"\nedition = "2021"\n` },
          { name: "src/lib.rs", content: `pub fn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n\n#[cfg(test)]\nmod tests {\n    use super::*;\n    #[test]\n    fn test_add() { assert_eq!(add(2, 3), 5); }\n}` },
        ],
      },
    ],
  },
  {
    id: "dotnet",
    emoji: "💜",
    label: ".NET / C#",
    description: "C# .NET 跨平台应用",
    color: "#ce93d8",
    types: [
      {
        id: "csharp-console",
        label: "C# Console",
        description: ".NET 控制台程序",
        extensions: ["ms-dotnettools.csharp"],
        setupType: "dotnet",
        templateFiles: [
          { name: "Program.cs", content: `using System;\n\nConsole.WriteLine("Hello, C#!");` },
          { name: "MyApp.csproj", content: `<Project Sdk="Microsoft.NET.Sdk">\n  <PropertyGroup>\n    <OutputType>Exe</OutputType>\n    <TargetFramework>net8.0</TargetFramework>\n  </PropertyGroup>\n</Project>` },
        ],
      },
      {
        id: "csharp-webapi",
        label: "ASP.NET Core Web API",
        description: "C# RESTful Web API 项目",
        extensions: ["ms-dotnettools.csharp"],
        setupType: "dotnet",
        templateFiles: [
          { name: "Program.cs", content: `var builder = WebApplication.CreateBuilder(args);\nbuilder.Services.AddControllers();\nvar app = builder.Build();\napp.MapControllers();\napp.Run();` },
        ],
      },
    ],
  },
  {
    id: "cpp",
    emoji: "⚙️",
    label: "C / C++",
    description: "系统级编程、嵌入式、游戏引擎",
    color: "#80cbc4",
    types: [
      {
        id: "c-general",
        label: "C 项目",
        description: "标准 C 语言项目，CMake 构建",
        extensions: ["ms-vscode.cpptools", "ms-vscode.cmake-tools"],
        templateFiles: [
          { name: "main.c", content: `#include <stdio.h>\n\nint main() {\n    printf("Hello, C!\\n");\n    return 0;\n}` },
          { name: "CMakeLists.txt", content: `cmake_minimum_required(VERSION 3.16)\nproject(my_project)\nset(CMAKE_C_STANDARD 17)\nadd_executable(my_project main.c)` },
        ],
      },
      {
        id: "cpp-general",
        label: "C++ 项目",
        description: "现代 C++17/20，CMake 构建",
        extensions: ["ms-vscode.cpptools", "ms-vscode.cmake-tools"],
        templateFiles: [
          { name: "main.cpp", content: `#include <iostream>\n\nint main() {\n    std::cout << "Hello, C++!" << std::endl;\n    return 0;\n}` },
          { name: "CMakeLists.txt", content: `cmake_minimum_required(VERSION 3.16)\nproject(my_project)\nset(CMAKE_CXX_STANDARD 17)\nadd_executable(my_project main.cpp)` },
        ],
      },
      {
        id: "cpp-arduino",
        label: "Arduino / 嵌入式",
        description: "Arduino / ESP32 嵌入式开发",
        extensions: ["ms-vscode.cpptools", "vsciot-vscode.vscode-arduino"],
        templateFiles: [
          { name: "main.ino", content: `void setup() {\n  Serial.begin(9600);\n  Serial.println("Hello Arduino!");\n}\n\nvoid loop() {\n  delay(1000);\n}` },
        ],
      },
    ],
  },
  {
    id: "php",
    emoji: "🐘",
    label: "PHP",
    description: "Laravel、Symfony、WordPress 等 PHP 项目",
    color: "#b39ddb",
    types: [
      {
        id: "php-general",
        label: "PHP 通用",
        description: "标准 PHP 脚本项目",
        extensions: ["bmewburn.vscode-intelephense-client"],
        templateFiles: [
          { name: "index.php", content: `<?php\n\necho "Hello, PHP!";\n` },
        ],
      },
      {
        id: "php-laravel",
        label: "Laravel",
        description: "Laravel MVC Web 框架",
        extensions: ["bmewburn.vscode-intelephense-client"],
        setupType: "composer",
        templateFiles: [
          { name: "composer.json", content: `{\n  "name": "my/laravel-app",\n  "require": {\n    "laravel/framework": "^11.0"\n  }\n}` },
        ],
      },
      {
        id: "php-wordpress",
        label: "WordPress 主题 / 插件",
        description: "WordPress 二次开发",
        extensions: ["bmewburn.vscode-intelephense-client"],
        templateFiles: [
          { name: "functions.php", content: `<?php\n// Theme functions\nadd_action('wp_enqueue_scripts', function() {\n    wp_enqueue_style('theme-style', get_stylesheet_uri());\n});\n` },
        ],
      },
    ],
  },
  {
    id: "ruby",
    emoji: "�",
    label: "Ruby",
    description: "Ruby / Rails / Sinatra Web 开发",
    color: "#ef9a9a",
    types: [
      {
        id: "ruby-general",
        label: "Ruby 通用",
        description: "标准 Ruby 脚本项目",
        extensions: ["shopify.ruby-lsp"],
        setupType: "ruby",
        templateFiles: [
          { name: "main.rb", content: `puts "Hello, Ruby!"` },
          { name: "Gemfile", content: `source "https://rubygems.org"\nruby ">= 3.0"` },
        ],
      },
      {
        id: "ruby-rails",
        label: "Ruby on Rails",
        description: "Rails MVC Web 框架",
        extensions: ["shopify.ruby-lsp"],
        setupType: "ruby",
        templateFiles: [
          { name: "Gemfile", content: `source "https://rubygems.org"\nruby ">= 3.0"\ngem "rails", ">= 7.0"` },
        ],
      },
      {
        id: "ruby-sinatra",
        label: "Sinatra",
        description: "轻量 Ruby Web 框架",
        extensions: ["shopify.ruby-lsp"],
        setupType: "ruby",
        templateFiles: [
          { name: "app.rb", content: `require "sinatra"\n\nget "/" do\n  "Hello, Sinatra!"\nend` },
          { name: "Gemfile", content: `source "https://rubygems.org"\ngem "sinatra"` },
        ],
      },
    ],
  },
  {
    id: "other",
    emoji: "📦",
    label: "其他",
    description: "Monorepo、Shell 脚本、Markdown 文档等",
    color: "#a5d6a7",
    types: [
      {
        id: "monorepo",
        label: "大项目 / Monorepo",
        description: "多语言混合或 Monorepo 大型项目",
        extensions: [],
        templateFiles: [
          { name: "README.md", content: `# Monorepo\n\n> 多模块大型项目\n\n## 模块列表\n\n- packages/\n- apps/\n` },
        ],
      },
      {
        id: "shell-script",
        label: "Shell 脚本",
        description: "Bash / Zsh 自动化脚本",
        extensions: ["timonwong.shellcheck"],
        templateFiles: [
          { name: "main.sh", content: `#!/usr/bin/env bash\nset -euo pipefail\n\necho "Hello, Shell!"\n` },
        ],
      },
      {
        id: "markdown-docs",
        label: "Markdown 文档",
        description: "文档、博客、Wiki 项目",
        extensions: ["yzhang.markdown-all-in-one"],
        templateFiles: [
          { name: "README.md", content: `# My Project\n\n> 项目说明文档\n\n## 快速开始\n\n...` },
        ],
      },
      {
        id: "empty",
        label: "空项目",
        description: "仅创建目录，不添加任何文件",
        extensions: [],
      },
    ],
  },
];
