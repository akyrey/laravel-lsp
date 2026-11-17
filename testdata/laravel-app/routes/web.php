<?php

use App\Models\User;
use Illuminate\Support\Facades\Route;

Route::get('/users/{user}', fn (User $user) => view('home', [
    'name' => $user->name,
    'app' => config('app.name'),
]))->name('users.show');
